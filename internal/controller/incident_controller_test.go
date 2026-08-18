package controller

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/storage"
	"github.com/wen/opentalon/internal/workflow"
)

type scriptedInvestigator struct {
	incidentID   string
	workflow     *workflow.IncidentWorkflow
	instructions []string
	run          func(int, string) error
}

func (s *scriptedInvestigator) IncidentID() string { return s.incidentID }

func (s *scriptedInvestigator) Investigate(_ context.Context, instruction string) error {
	s.instructions = append(s.instructions, instruction)
	if s.run == nil {
		return nil
	}
	return s.run(len(s.instructions), instruction)
}

func TestIncidentControllerRunsFromProtectedToResolved(t *testing.T) {
	service := executionPlatform("safe_fix")
	controller, _, database, investigator := incidentControllerForTest(t, service, func(instance *workflow.IncidentWorkflow) *scriptedInvestigator {
		return planSubmittingInvestigator(instance, "safe_fix")
	})
	defer database.Close()

	result, err := controller.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StopResolved, result.Reason)
	assert.Equal(t, workflow.StateResolved, result.Snapshot.State)
	assert.Len(t, investigator.instructions, 1)
	assert.Equal(t, 1, service.sideEffects)
	require.Len(t, result.Snapshot.Checkpoints, 1)
	assert.Equal(t, workflow.CheckpointSucceeded, result.Snapshot.Checkpoints[0].Decision)
}

func TestIncidentControllerRunsDynamicStagesWithResolvedOutputBinding(t *testing.T) {
	service := executionPlatform("refresh_route", "probe_route")
	service.executionResults["refresh_route"] = map[string]any{"route": map[string]any{"id": "route-new"}}
	service.executionResults["probe_route"] = map[string]any{"outcome": "healthy"}
	orchestrator, _, database, _ := incidentControllerForTest(t, service, func(instance *workflow.IncidentWorkflow) *scriptedInvestigator {
		value := &scriptedInvestigator{incidentID: instance.Snapshot().IncidentID, workflow: instance}
		value.run = func(_ int, _ string) error {
			_, err := instance.SubmitPlan(workflow.PlanDraft{
				Summary: "refresh and probe", RootCause: "stale route", EvidenceRefs: []string{"trace:dynamic"},
				Stages: []workflow.PlanStageDraft{
					{StageID: "refresh", Goal: "refresh route", Actions: []workflow.PlannedAction{{Key: "refresh-route", ToolName: "refresh_route"}},
						CheckpointPolicy: workflow.CheckpointPolicy{DefaultDecision: workflow.CheckpointContinue}},
					{StageID: "probe", Goal: "probe route", Actions: []workflow.PlannedAction{{Key: "probe-route", ToolName: "probe_route",
						ArgumentReferences: map[string]workflow.ActionOutputReference{"route_id": {
							SourceActionID: "refresh-route", OutputPath: "output.route.id", ExpectedType: workflow.ActionOutputString, Required: true,
						}}}}, CheckpointPolicy: workflow.CheckpointPolicy{Rules: []workflow.CheckpointRule{{
						SourceActionID: "probe-route", OutputPath: "output.outcome", Equals: "healthy",
						Decision: workflow.CheckpointSucceeded, Reason: "route is healthy",
					}}, DefaultDecision: workflow.CheckpointNeedsAgent}},
				},
			})
			return err
		}
		return value
	})
	defer database.Close()

	result, err := orchestrator.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StopResolved, result.Reason)
	assert.Equal(t, workflow.StateResolved, result.Snapshot.State)
	assert.Equal(t, 2, service.sideEffects)
	require.Len(t, result.Snapshot.Checkpoints, 2)
	assert.Equal(t, workflow.CheckpointContinue, result.Snapshot.Checkpoints[0].Decision)
	assert.Equal(t, workflow.CheckpointSucceeded, result.Snapshot.Checkpoints[1].Decision)
	require.Len(t, result.Snapshot.ActionResults, 2)

	var probeExecution *platform.RemediationRequest
	for index := range service.requests {
		request := &service.requests[index]
		if request.ToolName == "probe_route" && !request.DryRun {
			probeExecution = request
		}
	}
	require.NotNil(t, probeExecution)
	assert.Equal(t, "route-new", probeExecution.Arguments["route_id"])
}

func TestDynamicActionApprovalBindsResolvedArgumentsAndDigest(t *testing.T) {
	service := executionPlatform("refresh_route", "probe_route")
	service.capabilities[1].Risk = "medium"
	service.executionResults["refresh_route"] = map[string]any{"route": map[string]any{"id": "route-approved"}}
	service.executionResults["probe_route"] = map[string]any{"outcome": "healthy"}
	orchestrator, _, database, _ := incidentControllerForTest(t, service, func(instance *workflow.IncidentWorkflow) *scriptedInvestigator {
		value := &scriptedInvestigator{incidentID: instance.Snapshot().IncidentID, workflow: instance}
		value.run = func(_ int, _ string) error {
			_, err := instance.SubmitPlan(dynamicRouteDraft(workflow.CheckpointSucceeded))
			return err
		}
		return value
	})
	defer database.Close()

	waiting, err := orchestrator.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StopAwaitingApproval, waiting.Reason)
	require.NotNil(t, waiting.Snapshot.Plan)
	require.Len(t, waiting.Snapshot.ResolvedActions, 2)
	template := waiting.Snapshot.Plan.Stages[1].Actions[0]
	resolved := waiting.Snapshot.ResolvedActions[1]
	assert.NotEqual(t, template.Digest, resolved.Digest)
	pending, err := database.Approvals().ListPending(context.Background())
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, resolved.Digest, pending[0].ActionDigest)
	assert.Equal(t, "route-approved", pending[0].Arguments["route_id"])

	_, err = orchestrator.planProcessor.Approve(context.Background(), ApprovalRequest{
		PlanID: waiting.Snapshot.Plan.ID, ActionID: resolved.ActionID, ActionDigest: template.Digest, Approver: "oncall",
	})
	require.Error(t, err, "an approval for the unresolved template digest must not be reusable")
	_, err = orchestrator.planProcessor.Approve(context.Background(), ApprovalRequest{
		PlanID: waiting.Snapshot.Plan.ID, ActionID: resolved.ActionID, ActionDigest: resolved.Digest, Approver: "oncall",
	})
	require.NoError(t, err)
	result, err := orchestrator.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StopResolved, result.Reason)
}

func TestDynamicCheckpointNeedsAgentInvokesAgentWithNewActionEvidence(t *testing.T) {
	service := executionPlatform("inspect_route")
	service.executionResults["inspect_route"] = map[string]any{"outcome": "unknown"}
	orchestrator, _, database, investigator := incidentControllerForTest(t, service, func(instance *workflow.IncidentWorkflow) *scriptedInvestigator {
		value := &scriptedInvestigator{incidentID: instance.Snapshot().IncidentID, workflow: instance}
		value.run = func(run int, _ string) error {
			if run == 1 {
				_, err := instance.SubmitPlan(workflow.PlanDraft{
					Summary: "inspect route", RootCause: "unknown route state", EvidenceRefs: []string{"trace:dynamic"},
					Stages: []workflow.PlanStageDraft{{StageID: "inspect", Goal: "inspect", Actions: []workflow.PlannedAction{{Key: "inspect-route", ToolName: "inspect_route"}},
						CheckpointPolicy: workflow.CheckpointPolicy{DefaultDecision: workflow.CheckpointNeedsAgent, DefaultReason: "semantic judgment required"}}},
				})
				return err
			}
			_, err := instance.Apply(workflow.Event{Type: workflow.EventEscalated, Actor: workflow.ActorAgent, Reason: "manual review"})
			return err
		}
		return value
	})
	defer database.Close()

	result, err := orchestrator.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StopEscalated, result.Reason)
	require.Len(t, investigator.instructions, 2)
	assert.Contains(t, investigator.instructions[1], "semantic judgment required")
	assert.Contains(t, investigator.instructions[1], "evidence_refs=action:")
	require.Len(t, result.Snapshot.ActionResults, 1)
	assert.NotEmpty(t, result.Snapshot.ActionResults[0].EvidenceRef)
}

func dynamicRouteDraft(finalDecision workflow.CheckpointDecision) workflow.PlanDraft {
	return workflow.PlanDraft{
		Summary: "refresh and probe", RootCause: "stale route", EvidenceRefs: []string{"trace:dynamic"},
		Stages: []workflow.PlanStageDraft{
			{StageID: "refresh", Goal: "refresh route", Actions: []workflow.PlannedAction{{Key: "refresh-route", ToolName: "refresh_route"}},
				CheckpointPolicy: workflow.CheckpointPolicy{DefaultDecision: workflow.CheckpointContinue}},
			{StageID: "probe", Goal: "probe route", Actions: []workflow.PlannedAction{{Key: "probe-route", ToolName: "probe_route",
				ArgumentReferences: map[string]workflow.ActionOutputReference{"route_id": {
					SourceActionID: "refresh-route", OutputPath: "output.route.id", ExpectedType: workflow.ActionOutputString, Required: true,
				}}}}, CheckpointPolicy: workflow.CheckpointPolicy{DefaultDecision: finalDecision}},
		},
	}
}

func TestIncidentControllerStopsForActionApproval(t *testing.T) {
	service := executionPlatform("risky_fix")
	service.capabilities[0].Risk = "medium"
	controller, _, database, _ := incidentControllerForTest(t, service, func(instance *workflow.IncidentWorkflow) *scriptedInvestigator {
		return planSubmittingInvestigator(instance, "risky_fix")
	})
	defer database.Close()

	result, err := controller.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StopAwaitingApproval, result.Reason)
	assert.Equal(t, workflow.StateAwaitingApproval, result.Snapshot.State)
	pending, err := database.Approvals().ListPending(context.Background())
	require.NoError(t, err)
	assert.Len(t, pending, 1)
	assert.Equal(t, 0, service.sideEffects)
}

func TestIncidentControllerResumesAfterApproval(t *testing.T) {
	service := executionPlatform("risky_fix")
	service.capabilities[0].Risk = "medium"
	controller, _, database, _ := incidentControllerForTest(t, service, func(instance *workflow.IncidentWorkflow) *scriptedInvestigator {
		return planSubmittingInvestigator(instance, "risky_fix")
	})
	defer database.Close()

	waiting, err := controller.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, StopAwaitingApproval, waiting.Reason)
	require.NotNil(t, waiting.Snapshot.Plan)
	action := waiting.Snapshot.Plan.Stages[0].Actions[0]
	_, err = controller.planProcessor.Approve(context.Background(), ApprovalRequest{
		PlanID: waiting.Snapshot.Plan.ID, ActionID: action.ID, ActionDigest: action.Digest,
		Approver: "oncall", Reason: "verified dry run and rollback scope",
	})
	require.NoError(t, err)

	resumed, err := controller.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StopResolved, resumed.Reason)
	assert.Equal(t, workflow.StateResolved, resumed.Snapshot.State)
	assert.Equal(t, 1, service.sideEffects)
}

func TestIncidentControllerReinvestigatesWithExecutionFailure(t *testing.T) {
	service := executionPlatform("failing_fix")
	service.executionStatus = platform.OperationFailed
	controller, instance, database, investigator := incidentControllerForTest(t, service, func(instance *workflow.IncidentWorkflow) *scriptedInvestigator {
		value := &scriptedInvestigator{incidentID: instance.Snapshot().IncidentID, workflow: instance}
		value.run = func(run int, _ string) error {
			if run == 1 {
				_, err := instance.SubmitPlan(testPlanDraft("failing_fix"))
				return err
			}
			_, err := instance.Apply(workflow.Event{
				Type: workflow.EventEscalated, Actor: workflow.ActorAgent,
				Reason: "修复失败后没有其他安全动作",
			})
			return err
		}
		return value
	})
	defer database.Close()

	result, err := controller.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StopEscalated, result.Reason)
	assert.Equal(t, workflow.StateEscalated, instance.Snapshot().State)
	require.Len(t, investigator.instructions, 2)
	assert.Contains(t, investigator.instructions[1], "继续处理")
	assert.Contains(t, investigator.instructions[1], "上一执行阶段未成功")
	failures := instance.Snapshot().Failures
	require.NotEmpty(t, failures)
	assert.Equal(t, workflow.FailureStageActionExecution, failures[len(failures)-1].Stage)
	assert.Equal(t, workflow.FailureCategoryExecutionFailed, failures[len(failures)-1].Category)
	assert.Equal(t, "action_operation_failed", failures[len(failures)-1].Code)
}

func TestIncidentControllerRejectsInvestigatorWithoutWorkflowProgress(t *testing.T) {
	service := executionPlatform("unused_fix")
	controller, _, database, _ := incidentControllerForTest(t, service, func(instance *workflow.IncidentWorkflow) *scriptedInvestigator {
		return &scriptedInvestigator{incidentID: instance.Snapshot().IncidentID, workflow: instance}
	})
	defer database.Close()

	result, err := controller.Run(context.Background())
	assert.ErrorIs(t, err, ErrControllerNoProgress)
	assert.Equal(t, workflow.StateInvestigating, result.Snapshot.State)
}

func incidentControllerForTest(
	t *testing.T,
	service *idempotentExecutionPlatform,
	buildInvestigator func(*workflow.IncidentWorkflow) *scriptedInvestigator,
) (*IncidentController, *workflow.IncidentWorkflow, *storage.Storage, *scriptedInvestigator) {
	t.Helper()
	instance, err := workflow.NewIncidentWorkflow(workflow.Config{IncidentID: "controller-incident"})
	require.NoError(t, err)
	database, err := storage.OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "talon.db"))
	require.NoError(t, err)
	processor, err := NewPlanProcessor(service, instance,
		WithApprovalStore(database.Approvals()),
		WithExecutionStore(database.Executions(), "controller-worker", time.Second),
		WithAsyncExecution(fastAsyncExecutionConfig()))
	require.NoError(t, err)
	investigator := buildInvestigator(instance)
	controller, err := NewIncidentController(IncidentControllerConfig{
		Workflow: instance, Investigator: investigator, PlanProcessor: processor,
		WorkerRetryInterval: time.Millisecond,
	})
	require.NoError(t, err)
	return controller, instance, database, investigator
}

func planSubmittingInvestigator(instance *workflow.IncidentWorkflow, toolName string) *scriptedInvestigator {
	value := &scriptedInvestigator{incidentID: instance.Snapshot().IncidentID, workflow: instance}
	value.run = func(_ int, _ string) error {
		_, err := instance.SubmitPlan(testPlanDraft(toolName))
		return err
	}
	return value
}

func testPlanDraft(toolName string) workflow.PlanDraft {
	return workflow.PlanDraft{
		Summary: "repair incident", RootCause: "confirmed test failure",
		EvidenceRefs: []string{"trace:controller"},
		Stages: []workflow.PlanStageDraft{{StageID: "repair", Goal: "repair incident",
			Actions:          []workflow.PlannedAction{{Key: "repair", ToolName: toolName, Arguments: map[string]any{}}},
			CheckpointPolicy: workflow.CheckpointPolicy{DefaultDecision: workflow.CheckpointSucceeded}}},
	}
}

var _ Investigator = (*scriptedInvestigator)(nil)
