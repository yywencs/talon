package workflow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDynamicStageResolvesTypedOutputBeforeNextActionValidation(t *testing.T) {
	instance, submission := submitDynamicRoutePlan(t, ActionOutputString)
	first := resolveAndCompleteFirstDynamicStage(t, instance, submission, map[string]any{
		"route": map[string]any{"id": "route-new"},
	})
	assert.NotEmpty(t, first.EvidenceRef)

	checkpoint, err := instance.EvaluateCheckpoint()
	require.NoError(t, err)
	assert.Equal(t, CheckpointContinue, checkpoint.Decision)
	assert.Equal(t, "probe", checkpoint.NextStageID)
	assert.Equal(t, StatePlanned, instance.Snapshot().State)

	resolved, err := instance.ResolveCurrentStage()
	require.NoError(t, err)
	require.Len(t, resolved, 1)
	assert.Equal(t, "route-new", resolved[0].Arguments["route_id"])
	require.Len(t, resolved[0].Sources, 1)
	assert.Equal(t, "route_id", resolved[0].Sources[0].Argument)
	assert.Equal(t, first.ResultID, resolved[0].Sources[0].SourceResultID)
	assert.NotEqual(t, resolved[0].TemplateDigest, resolved[0].Digest)
}

func TestDynamicStageMissingRequiredOutputFailsBeforeDryRun(t *testing.T) {
	instance, submission := submitDynamicRoutePlan(t, ActionOutputString)
	resolveAndCompleteFirstDynamicStage(t, instance, submission, map[string]any{"route": map[string]any{}})
	_, err := instance.EvaluateCheckpoint()
	require.NoError(t, err)

	_, err = instance.ResolveCurrentStage()
	require.ErrorContains(t, err, "required_output_field_missing")
	snapshot := instance.Snapshot()
	assert.Equal(t, StateInvestigating, snapshot.State)
	require.Empty(t, snapshot.PlanDryRuns)
	require.NotEmpty(t, snapshot.Failures)
	assert.Equal(t, FailureStageArgumentResolution, snapshot.Failures[len(snapshot.Failures)-1].Stage)
	assert.Equal(t, "required_output_field_missing", snapshot.Failures[len(snapshot.Failures)-1].Code)
	require.NotEmpty(t, snapshot.Checkpoints)
	assert.Equal(t, CheckpointNeedsAgent, snapshot.Checkpoints[len(snapshot.Checkpoints)-1].Decision)
}

func TestDynamicStageOutputTypeMismatchFailsClosed(t *testing.T) {
	instance, submission := submitDynamicRoutePlan(t, ActionOutputString)
	resolveAndCompleteFirstDynamicStage(t, instance, submission, map[string]any{
		"route": map[string]any{"id": float64(42)},
	})
	_, err := instance.EvaluateCheckpoint()
	require.NoError(t, err)

	_, err = instance.ResolveCurrentStage()
	require.ErrorContains(t, err, "action_output_type_mismatch")
	snapshot := instance.Snapshot()
	assert.Equal(t, StateInvestigating, snapshot.State)
	require.Empty(t, snapshot.PlanDryRuns)
	assert.Equal(t, "action_output_type_mismatch", snapshot.Failures[len(snapshot.Failures)-1].Code)
}

func TestCheckpointOperationStatusUsesActionResultEnvelope(t *testing.T) {
	instance, err := NewIncidentWorkflow(Config{IncidentID: "operation-status-envelope"})
	require.NoError(t, err)
	_, err = instance.Apply(Event{Type: EventStartInvestigation, Actor: ActorController})
	require.NoError(t, err)
	submission, err := instance.SubmitPlan(PlanDraft{
		Summary: "verify operation status", RootCause: "confirmed", EvidenceRefs: []string{"evidence:status"},
		Stages: []PlanStageDraft{{
			StageID: "verify", Goal: "verify", Actions: []PlannedAction{{Key: "verify-action", ToolName: "verify"}},
			CheckpointPolicy: CheckpointPolicy{Rules: []CheckpointRule{{
				SourceActionID: "verify-action", OutputPath: "operation_status", Equals: "succeeded", Decision: CheckpointSucceeded,
			}}, DefaultDecision: CheckpointNeedsAgent},
		}},
	})
	require.NoError(t, err)
	resolved, err := instance.ResolveCurrentStage()
	require.NoError(t, err)
	_, err = instance.Apply(Event{Type: EventPlanApproved, Actor: ActorWorkflow})
	require.NoError(t, err)
	_, err = instance.RecordActionResult(ActionResult{
		PlanID: submission.Plan.ID, StageID: "verify", ActionID: resolved[0].ActionID,
		ActionDigest: resolved[0].Digest, OperationID: "operation", OperationStatus: "succeeded",
		Output: map[string]any{"status": "failed"},
	})
	require.NoError(t, err)
	_, err = instance.CompleteCurrentStage("done")
	require.NoError(t, err)
	checkpoint, err := instance.EvaluateCheckpoint()
	require.NoError(t, err)
	assert.Equal(t, CheckpointSucceeded, checkpoint.Decision)
	assert.Equal(t, StateResolved, instance.Snapshot().State)
}

func TestCheckpointRejectsAmbiguousLegacyStatusPath(t *testing.T) {
	instance, err := NewIncidentWorkflow(Config{IncidentID: "legacy-status-path"})
	require.NoError(t, err)
	_, err = instance.Apply(Event{Type: EventStartInvestigation, Actor: ActorController})
	require.NoError(t, err)
	_, err = instance.SubmitPlan(PlanDraft{
		Summary: "legacy status", RootCause: "confirmed", EvidenceRefs: []string{"evidence:status"},
		Stages: []PlanStageDraft{{
			StageID: "verify", Goal: "verify", Actions: []PlannedAction{{Key: "verify-action", ToolName: "verify"}},
			CheckpointPolicy: CheckpointPolicy{Rules: []CheckpointRule{{
				SourceActionID: "verify-action", OutputPath: "status", Equals: "succeeded", Decision: CheckpointSucceeded,
			}}},
		}},
	})
	require.ErrorContains(t, err, "operation_status or output.<field>")
	assert.Equal(t, StateInvestigating, instance.Snapshot().State)
}

func TestCheckpointRejectsComparisonTypeMismatch(t *testing.T) {
	for _, path := range []string{"operation_status", "output.outcome"} {
		t.Run(path, func(t *testing.T) {
			instance, err := NewIncidentWorkflow(Config{IncidentID: "checkpoint-type-" + strings.ReplaceAll(path, ".", "-")})
			require.NoError(t, err)
			_, err = instance.Apply(Event{Type: EventStartInvestigation, Actor: ActorController})
			require.NoError(t, err)
			_, err = instance.SubmitPlan(PlanDraft{
				Summary: "typed checkpoint", RootCause: "confirmed", EvidenceRefs: []string{"evidence:type"},
				Stages: []PlanStageDraft{{
					StageID: "verify", Goal: "verify", Actions: []PlannedAction{{Key: "verify-action", ToolName: "verify"}},
					CheckpointPolicy: CheckpointPolicy{Rules: []CheckpointRule{{
						SourceActionID: "verify-action", OutputPath: path, Equals: true, Decision: CheckpointSucceeded,
					}}},
				}},
			})
			require.ErrorContains(t, err, "requires a non-empty string comparison value")
			assert.Equal(t, StateInvestigating, instance.Snapshot().State)
		})
	}
}

func TestDynamicPlanRejectsContinueFromFinalStage(t *testing.T) {
	tests := []struct {
		name   string
		policy CheckpointPolicy
	}{
		{name: "rule", policy: CheckpointPolicy{Rules: []CheckpointRule{{
			SourceActionID: "recover-action", OutputPath: "operation_status", Equals: "succeeded", Decision: CheckpointContinue,
		}}}},
		{name: "default", policy: CheckpointPolicy{DefaultDecision: CheckpointContinue}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			instance, err := NewIncidentWorkflow(Config{IncidentID: "final-continue-" + test.name})
			require.NoError(t, err)
			_, err = instance.Apply(Event{Type: EventStartInvestigation, Actor: ActorController})
			require.NoError(t, err)
			_, err = instance.SubmitPlan(PlanDraft{
				Summary: "recover", RootCause: "confirmed", EvidenceRefs: []string{"evidence:recover"},
				Stages: []PlanStageDraft{{
					StageID: "recover", Goal: "recover", Actions: []PlannedAction{{Key: "recover-action", ToolName: "request_recovery"}},
					CheckpointPolicy: test.policy,
				}},
			})
			require.ErrorContains(t, err, "cannot")
			assert.Equal(t, StateInvestigating, instance.Snapshot().State)
		})
	}
}

func TestDynamicPlanRequiresFailClosedProbeCheckpoint(t *testing.T) {
	healthyRule := CheckpointRule{
		SourceActionID: "probe-action", OutputPath: "output.outcome", Equals: "healthy",
		Decision: CheckpointContinue, NextStageID: "recover",
	}
	tests := []struct {
		name      string
		policy    CheckpointPolicy
		wantError string
	}{
		{name: "empty policy", policy: CheckpointPolicy{}, wantError: "explicit fail-closed default_decision"},
		{name: "default continue", policy: CheckpointPolicy{Rules: []CheckpointRule{healthyRule}, DefaultDecision: CheckpointContinue}, wantError: "explicit fail-closed default_decision"},
		{name: "operation success continues", policy: CheckpointPolicy{Rules: []CheckpointRule{{
			SourceActionID: "probe-action", OutputPath: "operation_status", Equals: "succeeded", Decision: CheckpointContinue,
		}}, DefaultDecision: CheckpointNeedsAgent}, wantError: "unless the current probe output.outcome equals healthy"},
		{name: "hard stop continues", policy: CheckpointPolicy{Rules: []CheckpointRule{healthyRule, {
			SourceActionID: "probe-action", OutputPath: "output.outcome", Equals: "hard_stop", Decision: CheckpointContinue,
		}}, DefaultDecision: CheckpointNeedsAgent}, wantError: "unless the current probe output.outcome equals healthy"},
		{name: "healthy directly succeeds", policy: CheckpointPolicy{Rules: []CheckpointRule{{
			SourceActionID: "probe-action", OutputPath: "output.outcome", Equals: "healthy", Decision: CheckpointSucceeded,
		}}, DefaultDecision: CheckpointNeedsAgent}, wantError: "cannot select succeeded for a probe stage"},
		{name: "missing healthy rule", policy: CheckpointPolicy{DefaultDecision: CheckpointNeedsAgent}, wantError: "must define a healthy output.outcome continue rule"},
		{name: "safe", policy: CheckpointPolicy{Rules: []CheckpointRule{healthyRule, {
			SourceActionID: "probe-action", OutputPath: "output.outcome", Equals: "hard_stop", Decision: CheckpointNeedsAgent,
		}}, DefaultDecision: CheckpointNeedsAgent}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			instance, err := NewIncidentWorkflow(Config{IncidentID: "probe-checkpoint-" + test.name})
			require.NoError(t, err)
			_, err = instance.Apply(Event{Type: EventStartInvestigation, Actor: ActorController})
			require.NoError(t, err)
			_, err = instance.SubmitPlan(PlanDraft{
				Summary: "probe then recover", RootCause: "confirmed", EvidenceRefs: []string{"evidence:probe"},
				Stages: []PlanStageDraft{
					{StageID: "probe", Goal: "verify", Actions: []PlannedAction{{
						Key: "probe-action", Kind: ActionKindProbe, ToolName: "request_probe",
					}}, CheckpointPolicy: test.policy},
					{StageID: "recover", Goal: "recover", Actions: []PlannedAction{{Key: "recover-action", ToolName: "request_recovery"}},
						CheckpointPolicy: CheckpointPolicy{DefaultDecision: CheckpointSucceeded}},
				},
			})
			if test.wantError == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.wantError)
			assert.Equal(t, StateInvestigating, instance.Snapshot().State)
		})
	}
}

func TestDynamicPlanRequiresRecoveryImmediatelyAfterProbe(t *testing.T) {
	instance, err := NewIncidentWorkflow(Config{IncidentID: "probe-without-recovery"})
	require.NoError(t, err)
	_, err = instance.Apply(Event{Type: EventStartInvestigation, Actor: ActorController})
	require.NoError(t, err)
	_, err = instance.SubmitPlan(PlanDraft{
		Summary: "probe without recovery", RootCause: "confirmed", EvidenceRefs: []string{"evidence:probe"},
		Stages: []PlanStageDraft{
			{StageID: "probe", Goal: "verify", Actions: []PlannedAction{{
				Key: "probe-action", Kind: ActionKindProbe, ToolName: "request_probe",
			}}, CheckpointPolicy: CheckpointPolicy{Rules: []CheckpointRule{{
				SourceActionID: "probe-action", OutputPath: "output.outcome", Equals: "healthy",
				Decision: CheckpointContinue, NextStageID: "observe",
			}}, DefaultDecision: CheckpointNeedsAgent}},
			{StageID: "observe", Goal: "observe", Actions: []PlannedAction{{Key: "observe-action", ToolName: "observe"}},
				CheckpointPolicy: CheckpointPolicy{DefaultDecision: CheckpointSucceeded}},
		},
	})
	require.ErrorContains(t, err, "next linear stage to contain an explicit request_recovery action")
	assert.Equal(t, StateInvestigating, instance.Snapshot().State)
}

func TestDecisionCheckpointSupportsTerminalDecisions(t *testing.T) {
	for _, decision := range []CheckpointDecision{
		CheckpointSucceeded, CheckpointFailed, CheckpointEscalate, CheckpointBlocked, CheckpointNeedsAgent,
	} {
		t.Run(string(decision), func(t *testing.T) {
			instance, err := NewIncidentWorkflow(Config{IncidentID: "checkpoint-" + string(decision)})
			require.NoError(t, err)
			_, err = instance.Apply(Event{Type: EventStartInvestigation, Actor: ActorController})
			require.NoError(t, err)
			submission, err := instance.SubmitPlan(PlanDraft{
				Summary: "checkpoint", RootCause: "confirmed", EvidenceRefs: []string{"evidence:1"},
				Stages: []PlanStageDraft{{StageID: "only", Goal: "run", Actions: []PlannedAction{{Key: "run", ToolName: "run"}},
					CheckpointPolicy: CheckpointPolicy{DefaultDecision: decision, DefaultReason: "test decision"}}},
			})
			require.NoError(t, err)
			resolved, err := instance.ResolveCurrentStage()
			require.NoError(t, err)
			_, err = instance.Apply(Event{Type: EventPlanApproved, Actor: ActorWorkflow})
			require.NoError(t, err)
			_, err = instance.RecordActionResult(ActionResult{PlanID: submission.Plan.ID, StageID: "only",
				ActionID: resolved[0].ActionID, ActionDigest: resolved[0].Digest, OperationID: "operation",
				OperationStatus: "succeeded", Output: map[string]any{"outcome": "done"}})
			require.NoError(t, err)
			_, err = instance.CompleteCurrentStage("done")
			require.NoError(t, err)
			checkpoint, err := instance.EvaluateCheckpoint()
			require.NoError(t, err)
			assert.Equal(t, decision, checkpoint.Decision)
			want := map[CheckpointDecision]State{
				CheckpointSucceeded: StateResolved, CheckpointFailed: StateFailed,
				CheckpointEscalate: StateEscalated, CheckpointBlocked: StateBlocked,
				CheckpointNeedsAgent: StateInvestigating,
			}[decision]
			assert.Equal(t, want, instance.Snapshot().State)
		})
	}
}

func TestDynamicExecutionLimitsStopStageAndAgentResumeLoops(t *testing.T) {
	t.Run("max stages rejects oversized plan", func(t *testing.T) {
		instance, err := NewIncidentWorkflow(Config{IncidentID: "stage-limit", Limits: ExecutionLimits{MaxStages: 1, MaxAgentResumes: 1, MaxActions: 4}})
		require.NoError(t, err)
		_, err = instance.Apply(Event{Type: EventStartInvestigation, Actor: ActorController})
		require.NoError(t, err)
		_, err = instance.SubmitPlan(PlanDraft{Summary: "too many", RootCause: "confirmed", EvidenceRefs: []string{"evidence"},
			Stages: []PlanStageDraft{
				{StageID: "one", Goal: "one", Actions: []PlannedAction{{Key: "one", ToolName: "one"}}},
				{StageID: "two", Goal: "two", Actions: []PlannedAction{{Key: "two", ToolName: "two"}}},
			}})
		require.ErrorContains(t, err, "exceeding max_stages 1")
		assert.Equal(t, StateInvestigating, instance.Snapshot().State)
	})

	t.Run("max agent resumes fails closed", func(t *testing.T) {
		instance, err := NewIncidentWorkflow(Config{IncidentID: "resume-limit", Limits: ExecutionLimits{MaxStages: 4, MaxAgentResumes: 1, MaxActions: 4}})
		require.NoError(t, err)
		_, err = instance.Apply(Event{Type: EventStartInvestigation, Actor: ActorController})
		require.NoError(t, err)
		first := submitOneStageNeedsAgent(t, instance, "first")
		firstCheckpoint := completeOneStagePlan(t, instance, first)
		assert.Equal(t, CheckpointNeedsAgent, firstCheckpoint.Decision)
		assert.Equal(t, StateInvestigating, instance.Snapshot().State)

		second := submitOneStageNeedsAgent(t, instance, "second")
		secondCheckpoint := completeOneStagePlan(t, instance, second)
		assert.Equal(t, CheckpointFailed, secondCheckpoint.Decision)
		assert.Contains(t, secondCheckpoint.DecisionReason, "maximum agent resume count")
		assert.Equal(t, StateFailed, instance.Snapshot().State)
	})
}

func submitDynamicRoutePlan(t *testing.T, expectedType ActionOutputType) (*IncidentWorkflow, PlanSubmission) {
	t.Helper()
	instance, err := NewIncidentWorkflow(Config{IncidentID: "dynamic-route"})
	require.NoError(t, err)
	_, err = instance.Apply(Event{Type: EventStartInvestigation, Actor: ActorController})
	require.NoError(t, err)
	submission, err := instance.SubmitPlan(PlanDraft{
		Summary: "refresh and probe route", RootCause: "stale route", EvidenceRefs: []string{"trace:route"},
		Stages: []PlanStageDraft{
			{StageID: "refresh", Goal: "refresh route", Actions: []PlannedAction{{Key: "refresh-route", ToolName: "refresh_route"}},
				CheckpointPolicy: CheckpointPolicy{DefaultDecision: CheckpointContinue, DefaultReason: "route refreshed"}},
			{StageID: "probe", Goal: "probe route", Actions: []PlannedAction{{Key: "probe-route", ToolName: "probe_route",
				ArgumentReferences: map[string]ActionOutputReference{"route_id": {
					SourceActionID: "refresh-route", OutputPath: "output.route.id", ExpectedType: expectedType, Required: true,
				}}}}, CheckpointPolicy: CheckpointPolicy{DefaultDecision: CheckpointSucceeded}},
		},
	})
	require.NoError(t, err)
	return instance, submission
}

func resolveAndCompleteFirstDynamicStage(t *testing.T, instance *IncidentWorkflow, submission PlanSubmission, output map[string]any) ActionResult {
	t.Helper()
	resolved, err := instance.ResolveCurrentStage()
	require.NoError(t, err)
	require.Len(t, resolved, 1)
	_, err = instance.Apply(Event{Type: EventPlanApproved, Actor: ActorWorkflow})
	require.NoError(t, err)
	result, err := instance.RecordActionResult(ActionResult{
		PlanID: submission.Plan.ID, StageID: "refresh", ActionID: resolved[0].ActionID,
		ActionDigest: resolved[0].Digest, OperationID: "refresh-operation", OperationStatus: "succeeded", Output: output,
	})
	require.NoError(t, err)
	_, err = instance.CompleteCurrentStage("refresh complete")
	require.NoError(t, err)
	return result
}

func submitOneStageNeedsAgent(t *testing.T, instance *IncidentWorkflow, id string) PlanSubmission {
	t.Helper()
	submission, err := instance.SubmitPlan(PlanDraft{Summary: id, RootCause: "confirmed", EvidenceRefs: []string{"evidence:" + id},
		Stages: []PlanStageDraft{{StageID: id, Goal: id, Actions: []PlannedAction{{Key: id, ToolName: id}},
			CheckpointPolicy: CheckpointPolicy{DefaultDecision: CheckpointNeedsAgent, DefaultReason: "need semantic judgment"}}}})
	require.NoError(t, err)
	return submission
}

func completeOneStagePlan(t *testing.T, instance *IncidentWorkflow, submission PlanSubmission) DecisionCheckpoint {
	t.Helper()
	resolved, err := instance.ResolveCurrentStage()
	require.NoError(t, err)
	_, err = instance.Apply(Event{Type: EventPlanApproved, Actor: ActorWorkflow})
	require.NoError(t, err)
	_, err = instance.RecordActionResult(ActionResult{PlanID: submission.Plan.ID, StageID: submission.Plan.Stages[0].StageID,
		ActionID: resolved[0].ActionID, ActionDigest: resolved[0].Digest, OperationID: "operation-" + submission.Plan.ID,
		OperationStatus: "succeeded", Output: map[string]any{"outcome": "unknown"}})
	require.NoError(t, err)
	_, err = instance.CompleteCurrentStage("done")
	require.NoError(t, err)
	checkpoint, err := instance.EvaluateCheckpoint()
	require.NoError(t, err)
	return checkpoint
}
