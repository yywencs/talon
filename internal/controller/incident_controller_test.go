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

func TestIncidentControllerRunsFromProtectedThroughProbe(t *testing.T) {
	service := executionPlatform("safe_fix")
	controller, _, database, investigator := incidentControllerForTest(t, service, func(instance *workflow.IncidentWorkflow) *scriptedInvestigator {
		return planSubmittingInvestigator(instance, "safe_fix")
	})
	defer database.Close()

	result, err := controller.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StopRecovering, result.Reason)
	assert.Equal(t, workflow.StateRecovering, result.Snapshot.State)
	assert.Len(t, investigator.instructions, 1)
	assert.Equal(t, 1, service.sideEffects)
	assert.Equal(t, 1, service.probeCalls)
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
	action := waiting.Snapshot.Plan.Actions[0]
	_, err = controller.planProcessor.Approve(context.Background(), ApprovalRequest{
		PlanID: waiting.Snapshot.Plan.ID, ActionID: action.ID, ActionDigest: action.Digest,
		Approver: "oncall", Reason: "verified dry run and rollback scope",
	})
	require.NoError(t, err)

	resumed, err := controller.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StopRecovering, resumed.Reason)
	assert.Equal(t, workflow.StateRecovering, resumed.Snapshot.State)
	assert.Equal(t, 1, service.sideEffects)
}

func TestIncidentControllerPollsAsyncProbeUntilHealthy(t *testing.T) {
	service := executionPlatform("safe_fix")
	service.probeAsync = true
	controller, _, database, _ := incidentControllerForTest(t, service, func(instance *workflow.IncidentWorkflow) *scriptedInvestigator {
		return planSubmittingInvestigator(instance, "safe_fix")
	})
	defer database.Close()

	result, err := controller.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StopRecovering, result.Reason)
	assert.Equal(t, workflow.StateRecovering, result.Snapshot.State)
	assert.Equal(t, 1, service.probeCalls)
	require.NotEmpty(t, result.Snapshot.History)
	transition := result.Snapshot.History[len(result.Snapshot.History)-1]
	assert.Equal(t, "healthy", transition.Metadata["outcome"])
	assert.Equal(t, "safe-recovery", transition.Metadata["policy_id"])
}

func TestIncidentControllerReinvestigatesAfterProbeHardStop(t *testing.T) {
	service := executionPlatform("safe_fix")
	service.probeOutcome = "hard_stop"
	controller, instance, database, investigator := incidentControllerForTest(t, service, func(instance *workflow.IncidentWorkflow) *scriptedInvestigator {
		value := &scriptedInvestigator{incidentID: instance.Snapshot().IncidentID, workflow: instance}
		value.run = func(run int, _ string) error {
			if run == 1 {
				_, err := instance.SubmitPlan(testPlanDraft("safe_fix"))
				return err
			}
			_, err := instance.Apply(workflow.Event{
				Type: workflow.EventEscalated, Actor: workflow.ActorAgent,
				Reason: "探测硬停止后没有其他安全修复",
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
	assert.Contains(t, investigator.instructions[1], "hard-stop")
	assert.Equal(t, 1, service.probeCalls)
}

func TestIncidentControllerReinvestigatesAfterProbeTimeout(t *testing.T) {
	service := executionPlatform("safe_fix")
	service.probeAsync = true
	service.probeStayPending = true
	controller, instance, database, investigator := incidentControllerForTest(t, service, func(instance *workflow.IncidentWorkflow) *scriptedInvestigator {
		value := &scriptedInvestigator{incidentID: instance.Snapshot().IncidentID, workflow: instance}
		value.run = func(run int, _ string) error {
			if run == 1 {
				_, err := instance.SubmitPlan(testPlanDraft("safe_fix"))
				return err
			}
			_, err := instance.Apply(workflow.Event{
				Type: workflow.EventEscalated, Actor: workflow.ActorAgent,
				Reason: "探测超时后转人工确认",
			})
			return err
		}
		return value
	})
	defer database.Close()
	controller.probeProcessor.operationTimeout = 5 * time.Millisecond

	result, err := controller.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StopEscalated, result.Reason)
	assert.Equal(t, workflow.StateEscalated, instance.Snapshot().State)
	require.Len(t, investigator.instructions, 2)
	assert.Contains(t, investigator.instructions[1], "execution deadline")
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
	assert.Contains(t, investigator.instructions[1], "重新调查")
	assert.Contains(t, investigator.instructions[1], "上一阶段失败原因")
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
		Actions:      []workflow.PlannedAction{{ToolName: toolName, Arguments: map[string]any{}}},
		ProbeRouteID: "route-a", RecoveryPolicyID: "safe-recovery",
	}
}

var _ Investigator = (*scriptedInvestigator)(nil)
