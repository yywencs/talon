package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Restore rebuilds an independent in-memory Workflow without replaying actions,
// emitting transitions or resetting identities and budgets. The snapshot must
// contain the complete history produced by this Workflow implementation.
// Restoration alone does not reconcile newer approvals or platform operations.
func Restore(snapshot Snapshot) (*IncidentWorkflow, error) {
	if err := validateRestoreSnapshot(snapshot); err != nil {
		return nil, fmt.Errorf("restore workflow: %w", err)
	}
	history := make([]Transition, len(snapshot.History))
	for i, value := range snapshot.History {
		history[i] = cloneTransition(value)
	}
	return &IncidentWorkflow{
		incidentID: snapshot.IncidentID, intentIDPrefix: snapshot.IntentIDPrefix,
		state: snapshot.State, suspendedState: snapshot.SuspendedState, version: snapshot.Version,
		intent:             cloneExecutionIntentPointer(snapshot.ExecutionIntent),
		executionIntents:   cloneExecutionIntents(snapshot.ExecutionIntents),
		actionDryRuns:      cloneActionDryRuns(snapshot.ActionDryRuns),
		actionPolicies:     cloneActionPolicyDecisions(snapshot.ActionPolicies),
		actionApprovals:    cloneActionApprovals(snapshot.ActionApprovals),
		allActionDryRuns:   cloneActionDryRuns(snapshot.AllActionDryRuns),
		allActionPolicies:  cloneActionPolicyDecisions(snapshot.AllActionPolicies),
		allActionApprovals: cloneActionApprovals(snapshot.AllActionApprovals),
		activeStageIndex:   snapshot.ActiveStageIndex, resolvedActions: cloneResolvedActions(snapshot.ResolvedActions),
		actionResults: cloneActionResults(snapshot.ActionResults), checkpoints: cloneDecisionCheckpoints(snapshot.Checkpoints),
		limits: snapshot.Limits, stagesExecuted: snapshot.StagesExecuted,
		agentResumesUsed: snapshot.AgentResumesUsed, actionsAccepted: snapshot.ActionsAccepted,
		failures: cloneStageFailures(snapshot.Failures), history: history, now: time.Now,
	}, nil
}

func validateRestoreSnapshot(s Snapshot) error {
	if strings.TrimSpace(s.IncidentID) == "" || strings.TrimSpace(s.IntentIDPrefix) == "" {
		return fmt.Errorf("incident identity and intent prefix are required")
	}
	if err := validateState(s.State); err != nil {
		return err
	}
	if err := validateLimits(s.Limits); err != nil {
		return err
	}
	if s.StagesExecuted < 0 || s.StagesExecuted > s.Limits.MaxStages ||
		s.AgentResumesUsed < 0 || s.AgentResumesUsed > s.Limits.MaxAgentResumes ||
		s.ActionsAccepted < 0 || s.ActionsAccepted > s.Limits.MaxActions {
		return fmt.Errorf("invalid workflow budget counters")
	}
	if s.Version != uint64(len(s.History)) {
		return fmt.Errorf("workflow version does not match complete history")
	}
	state, suspended := StateProtected, State("")
	stages, resumes, activeStage := 0, 0, 0
	submissions := make([]string, 0)
	for i, h := range s.History {
		if h.Version != uint64(i+1) || h.From != state {
			return fmt.Errorf("discontinuous workflow history at version %d", h.Version)
		}
		rule, ok := transitionRules[state][h.Event]
		if h.Event == EventEscalated && state != StateResolved && state != StateEscalated {
			rule = transitionRule{to: StateEscalated, actors: actors(ActorAgent, ActorWorkflow, ActorController, ActorHuman)}
			ok = true
		}
		if _, allowed := rule.actors[h.Actor]; !ok || !allowed || rule.to != h.To {
			return fmt.Errorf("invalid history transition at version %d", h.Version)
		}
		if h.To == StateEscalated {
			suspended = state
		} else if state == StateEscalated {
			suspended = ""
		}
		state = h.To
		switch h.Event {
		case EventExecutionIntentSubmitted:
			activeStage = 0
			id := fmt.Sprintf("%s-intent-%d", s.IntentIDPrefix, h.Version)
			if h.Metadata["intent_id"] != id {
				return fmt.Errorf("intent identity does not match submission history")
			}
			submissions = append(submissions, id)
		case EventStageCheckpoint:
			stages++
		case EventCheckpointNeedsAgent:
			resumes++
		case EventCheckpointContinue:
			activeStage++
		}
	}
	if state != s.State || suspended != s.SuspendedState || stages != s.StagesExecuted || resumes != s.AgentResumesUsed {
		return fmt.Errorf("workflow state or counters do not match history")
	}
	if activeStage != s.ActiveStageIndex {
		return fmt.Errorf("active stage index does not match history")
	}
	if len(submissions) != len(s.ExecutionIntents) {
		return fmt.Errorf("intent history does not match submissions")
	}
	templates := make(map[string]restoreAction)
	for i, intent := range s.ExecutionIntents {
		if intent.ID != submissions[i] || len(intent.Stages) == 0 {
			return fmt.Errorf("invalid frozen intent history")
		}
		seenStages := make(map[string]bool)
		sequence := 0
		for j, stage := range intent.Stages {
			if stage.StageID == "" || seenStages[stage.StageID] || stage.Sequence != j+1 || len(stage.Actions) == 0 {
				return fmt.Errorf("invalid frozen stage in intent %q", intent.ID)
			}
			seenStages[stage.StageID] = true
			if err := validateCheckpointPolicy(stage.CheckpointPolicy); err != nil {
				return err
			}
			for _, action := range stage.Actions {
				sequence++
				if action.ID != fmt.Sprintf("%s-action-%d", intent.ID, sequence) || action.Digest == "" || action.ToolName == "" || !action.Kind.Valid() {
					return fmt.Errorf("invalid frozen action identity in intent %q", intent.ID)
				}
				templates[action.ID] = restoreAction{intent.ID, stage.StageID, action}
			}
		}
	}
	if len(templates) != s.ActionsAccepted {
		return fmt.Errorf("accepted action count does not match frozen intents")
	}
	if len(s.ExecutionIntents) == 0 {
		if s.ExecutionIntent != nil || s.ActiveStageIndex != 0 || len(s.ResolvedActions)+len(s.ActionResults)+len(s.ActionDryRuns)+len(s.ActionPolicies)+len(s.ActionApprovals)+len(s.AllActionDryRuns)+len(s.AllActionPolicies)+len(s.AllActionApprovals)+len(s.Checkpoints) != 0 {
			return fmt.Errorf("execution data exists without a frozen intent")
		}
		if s.State != StateProtected && s.State != StateInvestigating && s.State != StateEscalated {
			return fmt.Errorf("workflow state requires a frozen intent")
		}
		return nil
	}
	if s.ExecutionIntent == nil || !sameSnapshotJSON(*s.ExecutionIntent, s.ExecutionIntents[len(s.ExecutionIntents)-1]) {
		return fmt.Errorf("current intent does not match latest frozen intent")
	}
	if s.ActiveStageIndex < 0 || s.ActiveStageIndex >= len(s.ExecutionIntent.Stages) {
		return fmt.Errorf("active stage index is out of bounds")
	}
	return validateRestoreActions(s, templates)
}

type restoreAction struct {
	intentID string
	stageID  string
	action   IntendedAction
}

func validateRestoreActions(s Snapshot, templates map[string]restoreAction) error {
	resolved := make(map[string]ResolvedAction)
	for _, a := range s.ResolvedActions {
		t, ok := templates[a.ActionID]
		_, duplicate := resolved[a.ActionID]
		if !ok || duplicate || a.IntentID != t.intentID || a.StageID != t.stageID ||
			a.TemplateDigest != t.action.Digest || a.Digest == "" || a.ToolName != t.action.ToolName || a.Kind != t.action.Kind {
			return fmt.Errorf("resolved action does not match frozen template %q", a.ActionID)
		}
		// Digests are preserved, not regenerated from JSONB-normalized numeric text.
		resolved[a.ActionID] = a
	}
	for _, result := range s.ActionResults {
		a, ok := resolved[result.ActionID]
		if !ok || result.IntentID != a.IntentID || result.StageID != a.StageID || result.ActionDigest != a.Digest {
			return fmt.Errorf("action result does not match resolved action %q", result.ActionID)
		}
	}
	current := make(map[string]ResolvedAction)
	for id, a := range resolved {
		if a.IntentID == s.ExecutionIntent.ID && a.StageID == s.ExecutionIntent.Stages[s.ActiveStageIndex].StageID {
			current[id] = a
		}
	}
	dryRuns := make(map[string]ActionDryRun)
	for _, d := range s.ActionDryRuns {
		if !d.Status.valid() || d.IdempotencyKey == "" {
			return fmt.Errorf("invalid current dry run")
		}
		if err := validateActionDryRunFailure(d.Status, d.Failure); err != nil {
			return err
		}
		a, ok := current[d.ActionID]
		if _, duplicate := dryRuns[d.ActionID]; duplicate || !ok || d.IntentID != a.IntentID || d.ActionDigest != a.Digest {
			return fmt.Errorf("current dry run does not match current action")
		}
		dryRuns[d.ActionID] = d
	}
	policies := make(map[string]ActionPolicyDecision)
	for _, p := range s.ActionPolicies {
		a, ok := current[p.ActionID]
		d := dryRuns[p.ActionID]
		if err := validateActionPolicyDecision(p); err != nil {
			return err
		}
		if _, duplicate := policies[p.ActionID]; duplicate || !ok || p.IntentID != a.IntentID || p.ActionDigest != a.Digest || d.Status != ActionDryRunSucceeded || p.DryRunOperationID != d.OperationID {
			return fmt.Errorf("current policy does not match action and successful dry run")
		}
		policies[p.ActionID] = p
	}
	approved := make(map[string]bool)
	for _, a := range s.ActionApprovals {
		p, ok := policies[a.ActionID]
		if err := validateActionApproval(a); err != nil {
			return err
		}
		if _, duplicate := approved[a.ActionID]; duplicate || !ok || p.Outcome != ActionPolicyApprovalRequired || a.IntentID != p.IntentID || a.ActionDigest != p.ActionDigest {
			return fmt.Errorf("current approval does not match current policy")
		}
		approved[a.ActionID] = a.Decision == ActionApprovalApproved
	}
	if s.State == StateAwaitingApproval {
		pending := false
		if len(policies) != len(s.ExecutionIntent.Stages[s.ActiveStageIndex].Actions) {
			return fmt.Errorf("awaiting approval requires policies for every current action")
		}
		for id, p := range policies {
			if p.Outcome == ActionPolicyRejected {
				return fmt.Errorf("awaiting approval contains a rejected policy")
			}
			if p.Outcome == ActionPolicyApprovalRequired && !approved[id] {
				if _, decided := approved[id]; decided {
					return fmt.Errorf("awaiting approval contains a rejected approval")
				}
				pending = true
			}
		}
		if !pending {
			return fmt.Errorf("awaiting approval has no pending actions")
		}
	}
	return nil
}

func sameSnapshotJSON(a, b any) bool {
	x, err := json.Marshal(a)
	y, otherErr := json.Marshal(b)
	return err == nil && otherErr == nil && bytes.Equal(x, y)
}
