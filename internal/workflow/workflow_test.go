package workflow

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIncidentWorkflowHappyPath(t *testing.T) {
	fixedNow := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	workflow := newTestWorkflow(t, func() time.Time { return fixedNow })

	steps := []struct {
		event Event
		want  State
	}{
		{event: Event{Type: EventStartInvestigation, Actor: ActorController}, want: StateInvestigating},
		{event: Event{Type: EventPlanSubmitted, Actor: ActorAgent}, want: StatePlanned},
		{event: Event{Type: EventPlanApproved, Actor: ActorWorkflow}, want: StateRemediating},
		{event: Event{Type: EventStageSucceeded, Actor: ActorWorkflow}, want: StateProbing},
		{event: Event{Type: EventStageSucceeded, Actor: ActorController}, want: StateRecovering},
		{event: Event{Type: EventStageSucceeded, Actor: ActorController}, want: StateResolved},
	}

	for _, step := range steps {
		transition, err := workflow.Apply(step.event)
		require.NoError(t, err)
		assert.Equal(t, step.want, transition.To)
		assert.Equal(t, fixedNow, transition.At)
	}

	snapshot := workflow.Snapshot()
	assert.Equal(t, "incident-001", snapshot.IncidentID)
	assert.Equal(t, StateResolved, snapshot.State)
	assert.Equal(t, uint64(len(steps)), snapshot.Version)
	require.Len(t, snapshot.History, len(steps))
	assert.Equal(t, StateProtected, snapshot.History[0].From)
	assert.Equal(t, StateResolved, snapshot.History[len(snapshot.History)-1].To)
	assert.True(t, snapshot.State.Terminal())
}

func TestIncidentWorkflowApprovalAndReinvestigation(t *testing.T) {
	workflow := newTestWorkflow(t, nil)
	applyEvents(t, workflow,
		Event{Type: EventStartInvestigation, Actor: ActorController},
		Event{Type: EventPlanSubmitted, Actor: ActorAgent},
		Event{Type: EventApprovalRequired, Actor: ActorWorkflow},
	)
	assert.Equal(t, StateAwaitingApproval, workflow.Snapshot().State)

	_, err := workflow.Apply(Event{Type: EventPlanRejected, Actor: ActorHuman, Reason: "风险范围过大"})
	require.NoError(t, err)
	assert.Equal(t, StateReinvestigating, workflow.Snapshot().State)

	_, err = workflow.Apply(Event{Type: EventPlanSubmitted, Actor: ActorAgent})
	require.NoError(t, err)
	assert.Equal(t, StatePlanned, workflow.Snapshot().State)
}

func TestIncidentWorkflowCompensationAndEscalation(t *testing.T) {
	workflow := workflowAtRemediating(t)
	applyEvents(t, workflow,
		Event{Type: EventCompensationRequired, Actor: ActorWorkflow},
		Event{Type: EventStageFailed, Actor: ActorWorkflow, Reason: "rollback failed"},
	)

	snapshot := workflow.Snapshot()
	assert.Equal(t, StateEscalated, snapshot.State)
	assert.Equal(t, StateCompensating, snapshot.History[len(snapshot.History)-1].From)
	assert.Equal(t, "rollback failed", snapshot.History[len(snapshot.History)-1].Reason)
}

func TestIncidentWorkflowEscalationRequiresHumanResume(t *testing.T) {
	workflow := workflowAtRemediating(t)
	_, err := workflow.Apply(Event{Type: EventEscalated, Actor: ActorAgent, Reason: "需要更高权限"})
	require.NoError(t, err)
	assert.Equal(t, StateEscalated, workflow.Snapshot().State)
	assert.Equal(t, StateRemediating, workflow.Snapshot().SuspendedState)

	_, err = workflow.Apply(Event{Type: EventHumanResumed, Actor: ActorAgent})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrActorNotAllowed))
	assert.Equal(t, StateEscalated, workflow.Snapshot().State)

	_, err = workflow.Apply(Event{Type: EventHumanResumed, Actor: ActorHuman})
	require.NoError(t, err)
	assert.Equal(t, StateReinvestigating, workflow.Snapshot().State)
	assert.Empty(t, workflow.Snapshot().SuspendedState)
}

func TestIncidentWorkflowRejectsInvalidTransitionAndActor(t *testing.T) {
	workflow := newTestWorkflow(t, nil)

	_, err := workflow.Apply(Event{Type: EventStartInvestigation, Actor: ActorAgent})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrActorNotAllowed))
	assert.Equal(t, StateProtected, workflow.Snapshot().State)
	assert.Zero(t, workflow.Snapshot().Version)

	_, err = workflow.Apply(Event{Type: EventStageSucceeded, Actor: ActorController})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidTransition))
	assert.Equal(t, StateProtected, workflow.Snapshot().State)
}

func TestIncidentWorkflowAgentActionWhitelist(t *testing.T) {
	workflow := newTestWorkflow(t, nil)

	require.NoError(t, workflow.AuthorizeAgentAction(AgentActionEscalate))
	assert.ErrorIs(t, workflow.AuthorizeAgentAction(AgentActionRead), ErrAgentActionDenied)
	assert.ErrorIs(t, workflow.AuthorizeAgentAction(AgentActionSubmitPlan), ErrAgentActionDenied)

	applyEvents(t, workflow, Event{Type: EventStartInvestigation, Actor: ActorController})
	require.NoError(t, workflow.AuthorizeAgentAction(AgentActionRead))
	require.NoError(t, workflow.AuthorizeAgentAction(AgentActionQueryOperation))
	require.NoError(t, workflow.AuthorizeAgentAction(AgentActionSubmitPlan))

	applyEvents(t, workflow, Event{Type: EventPlanSubmitted, Actor: ActorAgent})
	require.NoError(t, workflow.AuthorizeAgentAction(AgentActionRead))
	assert.ErrorIs(t, workflow.AuthorizeAgentAction(AgentActionSubmitPlan), ErrAgentActionDenied)

	applyEvents(t, workflow,
		Event{Type: EventPlanApproved, Actor: ActorWorkflow},
		Event{Type: EventStageSucceeded, Actor: ActorWorkflow},
	)
	require.NoError(t, workflow.AuthorizeAgentAction(AgentActionRead))
	require.NoError(t, workflow.AuthorizeAgentAction(AgentActionQueryOperation))
	assert.ErrorIs(t, workflow.AuthorizeAgentAction(AgentActionSubmitPlan), ErrAgentActionDenied)
}

func TestIncidentWorkflowAllowedAgentActionsAreStable(t *testing.T) {
	workflow := newTestWorkflow(t, nil)
	applyEvents(t, workflow, Event{Type: EventStartInvestigation, Actor: ActorController})

	assert.Equal(t, []AgentAction{
		AgentActionEscalate,
		AgentActionManageSkill,
		AgentActionQueryOperation,
		AgentActionRead,
		AgentActionSubmitPlan,
	}, workflow.AllowedAgentActions())
}

func TestSkillEventsAreAuditedWithoutLeavingInvestigation(t *testing.T) {
	instance := newTestWorkflow(t, nil)
	applyEvents(t, instance, Event{Type: EventStartInvestigation, Actor: ActorController})

	loaded, err := instance.Apply(Event{Type: EventSkillLoaded, Actor: ActorAgent, Metadata: map[string]string{"skill_name": "mapping-diagnosis"}})
	require.NoError(t, err)
	assert.Equal(t, StateInvestigating, loaded.From)
	assert.Equal(t, StateInvestigating, loaded.To)
	unloaded, err := instance.Apply(Event{Type: EventSkillUnloaded, Actor: ActorAgent, Metadata: map[string]string{"skill_name": "mapping-diagnosis"}})
	require.NoError(t, err)
	assert.Equal(t, StateInvestigating, unloaded.To)
	assert.Equal(t, uint64(3), instance.Snapshot().Version)
}

func TestIncidentWorkflowSubmitPlanFreezesDraft(t *testing.T) {
	fixedNow := time.Date(2026, 8, 10, 9, 5, 0, 0, time.UTC)
	workflow := newTestWorkflow(t, func() time.Time { return fixedNow })
	applyEvents(t, workflow, Event{Type: EventStartInvestigation, Actor: ActorController})
	draft := PlanDraft{
		Summary: "回滚错误的 Mapping 配置", RootCause: "mapping schema regression",
		EvidenceRefs: []string{"log:invalid_parameter_type", "change:mapping-v2"},
		Actions: []PlannedAction{{
			ToolName:  "rollback_mapping",
			Arguments: map[string]any{"target_version": "mapping-v1"},
		}},
		ProbeRouteID: "route-a", RecoveryPolicyID: "default-safe-recovery",
	}

	submission, err := workflow.SubmitPlan(draft)
	require.NoError(t, err)
	assert.Equal(t, StatePlanned, workflow.Snapshot().State)
	assert.Equal(t, "incident-001-plan-2", submission.Plan.ID)
	assert.Equal(t, fixedNow, submission.Plan.SubmittedAt)
	assert.Equal(t, submission.Plan.ID, submission.Transition.Metadata["plan_id"])

	draft.Actions[0].Arguments["target_version"] = "mutated"
	snapshot := workflow.Snapshot()
	require.NotNil(t, snapshot.Plan)
	require.Len(t, snapshot.Plan.Actions, 1)
	assert.Equal(t, "incident-001-plan-2-action-1", snapshot.Plan.Actions[0].ID)
	assert.NotEmpty(t, snapshot.Plan.Actions[0].Digest)
	assert.Equal(t, "mapping-v1", snapshot.Plan.Actions[0].Arguments["target_version"])
	_, err = workflow.SubmitPlan(draft)
	assert.ErrorIs(t, err, ErrAgentActionDenied)
}

func TestIncidentWorkflowUsesIndependentPlanIDPrefix(t *testing.T) {
	instance, err := NewIncidentWorkflow(Config{IncidentID: "scenario-001", PlanIDPrefix: "run-abc"})
	require.NoError(t, err)
	applyEvents(t, instance, Event{Type: EventStartInvestigation, Actor: ActorController})
	submission, err := instance.SubmitPlan(PlanDraft{
		Summary: "repair", RootCause: "cause", EvidenceRefs: []string{"evidence"},
		Actions:      []PlannedAction{{ToolName: "repair", Arguments: map[string]any{"id": "value"}}},
		ProbeRouteID: "route-a", RecoveryPolicyID: "safe",
	})
	require.NoError(t, err)
	assert.Equal(t, "scenario-001", instance.Snapshot().IncidentID)
	assert.Equal(t, "run-abc-plan-2", submission.Plan.ID)
	assert.Equal(t, "run-abc-plan-2-action-1", submission.Plan.Actions[0].ID)
}

func TestIncidentWorkflowSubmitPlanValidatesRequiredFields(t *testing.T) {
	workflow := newTestWorkflow(t, nil)
	applyEvents(t, workflow, Event{Type: EventStartInvestigation, Actor: ActorController})

	_, err := workflow.SubmitPlan(PlanDraft{})
	require.Error(t, err)
	assert.Equal(t, StateInvestigating, workflow.Snapshot().State)
	assert.Zero(t, workflow.Snapshot().Plan)
}

func TestIncidentWorkflowSnapshotRetainsEverySubmittedPlan(t *testing.T) {
	instance := newTestWorkflow(t, nil)
	applyEvents(t, instance, Event{Type: EventStartInvestigation, Actor: ActorController})
	draft := PlanDraft{Summary: "first plan", RootCause: "first hypothesis", EvidenceRefs: []string{"log:first"}, Actions: []PlannedAction{{ToolName: "repair", Arguments: map[string]any{}}}, ProbeRouteID: "route-a", RecoveryPolicyID: "safe"}
	first, err := instance.SubmitPlan(draft)
	require.NoError(t, err)
	_, err = instance.Apply(Event{Type: EventPlanRejected, Actor: ActorWorkflow, Reason: "probe produced contrary evidence"})
	require.NoError(t, err)
	draft.Summary, draft.RootCause, draft.EvidenceRefs = "second plan", "revised hypothesis", []string{"log:first", "trace:new"}
	second, err := instance.SubmitPlan(draft)
	require.NoError(t, err)

	snapshot := instance.Snapshot()
	require.Len(t, snapshot.Plans, 2)
	assert.Equal(t, first.Plan.ID, snapshot.Plans[0].ID)
	assert.Equal(t, second.Plan.ID, snapshot.Plans[1].ID)
	assert.Equal(t, second.Plan.ID, snapshot.Plan.ID)
	snapshot.Plans[0].EvidenceRefs[0] = "mutated"
	assert.Equal(t, "log:first", instance.Snapshot().Plans[0].EvidenceRefs[0])
}

func TestIncidentWorkflowSnapshotDoesNotShareMetadata(t *testing.T) {
	workflow := newTestWorkflow(t, nil)
	metadata := map[string]string{"trigger": "error_rate"}
	_, err := workflow.Apply(Event{Type: EventStartInvestigation, Actor: ActorController, Metadata: metadata})
	require.NoError(t, err)
	metadata["trigger"] = "changed"

	first := workflow.Snapshot()
	assert.Equal(t, "error_rate", first.History[0].Metadata["trigger"])
	first.History[0].Metadata["trigger"] = "mutated snapshot"
	assert.Equal(t, "error_rate", workflow.Snapshot().History[0].Metadata["trigger"])
}

func TestEveryWorkflowStateHasAgentActionPolicy(t *testing.T) {
	for state := range validStates {
		_, exists := stateAgentActions[state]
		assert.Truef(t, exists, "state %s must define an agent action policy", state)
	}
}

func newTestWorkflow(t *testing.T, now func() time.Time) *IncidentWorkflow {
	t.Helper()
	workflow, err := NewIncidentWorkflow(Config{IncidentID: "incident-001", Now: now})
	require.NoError(t, err)
	return workflow
}

func workflowAtRemediating(t *testing.T) *IncidentWorkflow {
	t.Helper()
	workflow := newTestWorkflow(t, nil)
	applyEvents(t, workflow,
		Event{Type: EventStartInvestigation, Actor: ActorController},
		Event{Type: EventPlanSubmitted, Actor: ActorAgent},
		Event{Type: EventPlanApproved, Actor: ActorWorkflow},
	)
	return workflow
}

func applyEvents(t *testing.T, workflow *IncidentWorkflow, events ...Event) {
	t.Helper()
	for _, event := range events {
		_, err := workflow.Apply(event)
		require.NoError(t, err)
	}
}
