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
		{event: Event{Type: EventExecutionIntentSubmitted, Actor: ActorAgent}, want: StateValidating},
		{event: Event{Type: EventExecutionAuthorized, Actor: ActorWorkflow}, want: StateExecuting},
		{event: Event{Type: EventStageCheckpoint, Actor: ActorWorkflow}, want: StateCheckpoint},
		{event: Event{Type: EventCheckpointSucceeded, Actor: ActorWorkflow}, want: StateResolved},
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
		Event{Type: EventExecutionIntentSubmitted, Actor: ActorAgent},
		Event{Type: EventApprovalRequired, Actor: ActorWorkflow},
	)
	assert.Equal(t, StateAwaitingApproval, workflow.Snapshot().State)

	_, err := workflow.Apply(Event{Type: EventExecutionIntentRejected, Actor: ActorHuman, Reason: "风险范围过大"})
	require.NoError(t, err)
	assert.Equal(t, StateInvestigating, workflow.Snapshot().State)

	_, err = workflow.Apply(Event{Type: EventExecutionIntentSubmitted, Actor: ActorAgent})
	require.NoError(t, err)
	assert.Equal(t, StateValidating, workflow.Snapshot().State)
}

func TestIncidentWorkflowEscalationRequiresHumanResume(t *testing.T) {
	workflow := workflowAtRemediating(t)
	_, err := workflow.Apply(Event{Type: EventEscalated, Actor: ActorAgent, Reason: "需要更高权限"})
	require.NoError(t, err)
	assert.Equal(t, StateEscalated, workflow.Snapshot().State)
	assert.Equal(t, StateExecuting, workflow.Snapshot().SuspendedState)

	_, err = workflow.Apply(Event{Type: EventHumanResumed, Actor: ActorAgent})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrActorNotAllowed))
	assert.Equal(t, StateEscalated, workflow.Snapshot().State)

	_, err = workflow.Apply(Event{Type: EventHumanResumed, Actor: ActorHuman})
	require.NoError(t, err)
	assert.Equal(t, StateInvestigating, workflow.Snapshot().State)
	assert.Empty(t, workflow.Snapshot().SuspendedState)
}

func TestIncidentWorkflowRejectsInvalidTransitionAndActor(t *testing.T) {
	workflow := newTestWorkflow(t, nil)

	_, err := workflow.Apply(Event{Type: EventStartInvestigation, Actor: ActorAgent})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrActorNotAllowed))
	assert.Equal(t, StateProtected, workflow.Snapshot().State)
	assert.Zero(t, workflow.Snapshot().Version)

	_, err = workflow.Apply(Event{Type: EventStageCheckpoint, Actor: ActorController})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidTransition))
	assert.Equal(t, StateProtected, workflow.Snapshot().State)
}

func TestIncidentWorkflowAgentActionWhitelist(t *testing.T) {
	workflow := newTestWorkflow(t, nil)

	require.NoError(t, workflow.AuthorizeAgentAction(AgentActionEscalate))
	assert.ErrorIs(t, workflow.AuthorizeAgentAction(AgentActionRead), ErrAgentActionDenied)
	assert.ErrorIs(t, workflow.AuthorizeAgentAction(AgentActionSubmitExecutionIntent), ErrAgentActionDenied)

	applyEvents(t, workflow, Event{Type: EventStartInvestigation, Actor: ActorController})
	require.NoError(t, workflow.AuthorizeAgentAction(AgentActionRead))
	require.NoError(t, workflow.AuthorizeAgentAction(AgentActionQueryOperation))
	require.NoError(t, workflow.AuthorizeAgentAction(AgentActionSubmitExecutionIntent))

	applyEvents(t, workflow, Event{Type: EventExecutionIntentSubmitted, Actor: ActorAgent})
	require.NoError(t, workflow.AuthorizeAgentAction(AgentActionRead))
	assert.ErrorIs(t, workflow.AuthorizeAgentAction(AgentActionSubmitExecutionIntent), ErrAgentActionDenied)

	applyEvents(t, workflow,
		Event{Type: EventExecutionAuthorized, Actor: ActorWorkflow},
		Event{Type: EventStageCheckpoint, Actor: ActorWorkflow},
	)
	require.NoError(t, workflow.AuthorizeAgentAction(AgentActionRead))
	require.NoError(t, workflow.AuthorizeAgentAction(AgentActionQueryOperation))
	assert.ErrorIs(t, workflow.AuthorizeAgentAction(AgentActionSubmitExecutionIntent), ErrAgentActionDenied)
}

func TestIncidentWorkflowAllowedAgentActionsAreStable(t *testing.T) {
	workflow := newTestWorkflow(t, nil)
	applyEvents(t, workflow, Event{Type: EventStartInvestigation, Actor: ActorController})

	assert.Equal(t, []AgentAction{
		AgentActionEscalate,
		AgentActionManageSkill,
		AgentActionQueryOperation,
		AgentActionRead,
		AgentActionRecallEvidence,
		AgentActionSubmitExecutionIntent,
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

func TestIncidentWorkflowSubmitExecutionIntentFreezesDraft(t *testing.T) {
	fixedNow := time.Date(2026, 8, 10, 9, 5, 0, 0, time.UTC)
	workflow := newTestWorkflow(t, func() time.Time { return fixedNow })
	applyEvents(t, workflow, Event{Type: EventStartInvestigation, Actor: ActorController})
	draft := ExecutionIntentDraft{
		Summary: "回滚错误的 Mapping 配置", RootCause: "mapping schema regression",
		EvidenceRefs: []string{"log:invalid_parameter_type", "change:mapping-v2"},
		Stages: []ExecutionStageDraft{{StageID: "rollback", Goal: "rollback mapping", Actions: []IntendedAction{{
			Key: "rollback-mapping", ToolName: "rollback_mapping",
			Arguments: map[string]any{"target_version": "mapping-v1"},
		}}}},
	}

	submission, err := workflow.SubmitExecutionIntent(draft)
	require.NoError(t, err)
	assert.Equal(t, StateValidating, workflow.Snapshot().State)
	assert.Equal(t, "incident-001-intent-2", submission.ExecutionIntent.ID)
	assert.Equal(t, fixedNow, submission.ExecutionIntent.SubmittedAt)
	assert.Equal(t, submission.ExecutionIntent.ID, submission.Transition.Metadata["intent_id"])

	draft.Stages[0].Actions[0].Arguments["target_version"] = "mutated"
	snapshot := workflow.Snapshot()
	require.NotNil(t, snapshot.ExecutionIntent)
	require.Len(t, snapshot.ExecutionIntent.Stages, 1)
	require.Len(t, snapshot.ExecutionIntent.Stages[0].Actions, 1)
	assert.Equal(t, "incident-001-intent-2-action-1", snapshot.ExecutionIntent.Stages[0].Actions[0].ID)
	assert.NotEmpty(t, snapshot.ExecutionIntent.Stages[0].Actions[0].Digest)
	assert.Equal(t, "mapping-v1", snapshot.ExecutionIntent.Stages[0].Actions[0].Arguments["target_version"])
	_, err = workflow.SubmitExecutionIntent(draft)
	assert.ErrorIs(t, err, ErrAgentActionDenied)
}

func TestIncidentWorkflowUsesIndependentIntentIDPrefix(t *testing.T) {
	instance, err := NewIncidentWorkflow(Config{IncidentID: "scenario-001", IntentIDPrefix: "run-abc"})
	require.NoError(t, err)
	applyEvents(t, instance, Event{Type: EventStartInvestigation, Actor: ActorController})
	submission, err := instance.SubmitExecutionIntent(ExecutionIntentDraft{
		Summary: "repair", RootCause: "cause", EvidenceRefs: []string{"evidence"},
		Stages: []ExecutionStageDraft{{StageID: "repair", Goal: "repair", Actions: []IntendedAction{{
			Key: "repair", ToolName: "repair", Arguments: map[string]any{"id": "value"},
		}}}},
	})
	require.NoError(t, err)
	assert.Equal(t, "scenario-001", instance.Snapshot().IncidentID)
	assert.Equal(t, "run-abc-intent-2", submission.ExecutionIntent.ID)
	assert.Equal(t, "run-abc-intent-2-action-1", submission.ExecutionIntent.Stages[0].Actions[0].ID)
}

func TestIncidentWorkflowSubmitExecutionIntentValidatesRequiredFields(t *testing.T) {
	workflow := newTestWorkflow(t, nil)
	applyEvents(t, workflow, Event{Type: EventStartInvestigation, Actor: ActorController})

	_, err := workflow.SubmitExecutionIntent(ExecutionIntentDraft{})
	require.Error(t, err)
	assert.Equal(t, StateInvestigating, workflow.Snapshot().State)
	assert.Zero(t, workflow.Snapshot().ExecutionIntent)
}

func TestIncidentWorkflowSnapshotRetainsEverySubmittedIntent(t *testing.T) {
	instance := newTestWorkflow(t, nil)
	applyEvents(t, instance, Event{Type: EventStartInvestigation, Actor: ActorController})
	draft := ExecutionIntentDraft{Summary: "first intent", RootCause: "first hypothesis", EvidenceRefs: []string{"log:first"},
		Stages: []ExecutionStageDraft{{StageID: "repair", Goal: "repair", Actions: []IntendedAction{{Key: "repair", ToolName: "repair", Arguments: map[string]any{}}}}}}
	first, err := instance.SubmitExecutionIntent(draft)
	require.NoError(t, err)
	_, err = instance.Apply(Event{Type: EventExecutionIntentRejected, Actor: ActorWorkflow, Reason: "probe produced contrary evidence"})
	require.NoError(t, err)
	draft.Summary, draft.RootCause, draft.EvidenceRefs = "second intent", "revised hypothesis", []string{"log:first", "trace:new"}
	second, err := instance.SubmitExecutionIntent(draft)
	require.NoError(t, err)

	snapshot := instance.Snapshot()
	require.Len(t, snapshot.ExecutionIntents, 2)
	assert.Equal(t, first.ExecutionIntent.ID, snapshot.ExecutionIntents[0].ID)
	assert.Equal(t, second.ExecutionIntent.ID, snapshot.ExecutionIntents[1].ID)
	assert.Equal(t, second.ExecutionIntent.ID, snapshot.ExecutionIntent.ID)
	snapshot.ExecutionIntents[0].EvidenceRefs[0] = "mutated"
	assert.Equal(t, "log:first", instance.Snapshot().ExecutionIntents[0].EvidenceRefs[0])
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

func TestIncidentWorkflowRejectsInvalidRetrySemantics(t *testing.T) {
	instance := newTestWorkflow(t, nil)
	_, err := instance.RecordFailure(StageFailure{
		Stage: FailureStageActionExecution, Category: FailureCategoryPlatformUnavailable,
		Code: "remediation_query_failed", SafeSummary: "暂时无法查询执行 Operation",
		NextAction: FailureNextRetry, Retryable: false,
	})
	require.ErrorContains(t, err, "retry next action requires retryable failure")
	assert.Empty(t, instance.Snapshot().Failures)
}

func TestIncidentWorkflowRejectsFailureFromWrongStage(t *testing.T) {
	instance := workflowAtRemediating(t)
	_, err := instance.RecordFailure(StageFailure{
		Stage: FailureStageCheckpoint, Category: FailureCategoryPlatformUnavailable,
		Code: "checkpoint_query_failed", SafeSummary: "暂时无法查询 Checkpoint Operation",
		NextAction: FailureNextRetry, Retryable: true,
	})
	require.ErrorContains(t, err, "does not match workflow state")
	assert.Empty(t, instance.Snapshot().Failures)
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
		Event{Type: EventExecutionIntentSubmitted, Actor: ActorAgent},
		Event{Type: EventExecutionAuthorized, Actor: ActorWorkflow},
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
