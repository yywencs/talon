package controller

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/scenario"
	"github.com/wen/opentalon/internal/simulator"
	"github.com/wen/opentalon/internal/workflow"
)

type recordingPlatform struct {
	platform.ToolOpsPlatform
	requests  []platform.RemediationRequest
	operation platform.Operation
	err       error
}

func (p *recordingPlatform) ExecuteRemediation(_ context.Context, request platform.RemediationRequest) (platform.Operation, error) {
	p.requests = append(p.requests, request)
	return p.operation, p.err
}

func TestPlanProcessorDryRunRecordsSuccessAndIsIdempotent(t *testing.T) {
	instance := plannedWorkflow(t, "incident-001", map[string]any{
		"tool_id": "generate_image", "target_version": "mapping-v1",
		"expected_version": "mapping-v2", "idempotency_key": "rollback-001",
		"dry_run": false,
	})
	service := &recordingPlatform{operation: platform.Operation{
		ID: "operation-dry-run-001", Status: platform.OperationSucceeded,
		Message: "dry run completed", Result: map[string]any{"dry_run": true},
	}}
	processor, err := NewPlanProcessor(service, instance)
	require.NoError(t, err)

	result, err := processor.DryRun(context.Background())
	require.NoError(t, err)
	require.Len(t, service.requests, 1)
	request := service.requests[0]
	assert.True(t, request.DryRun)
	assert.Equal(t, "incident-001-plan-2:dry-run", request.IdempotencyKey)
	assert.Equal(t, "mapping-v2", request.ExpectedVersion)
	assert.Equal(t, map[string]any{"tool_id": "generate_image", "target_version": "mapping-v1"}, request.Arguments)
	assert.Equal(t, workflow.PlanDryRunSucceeded, result.Status)

	snapshot := instance.Snapshot()
	assert.Equal(t, workflow.StatePlanned, snapshot.State)
	require.NotNil(t, snapshot.PlanDryRun)
	assert.Equal(t, "operation-dry-run-001", snapshot.PlanDryRun.OperationID)
	assert.Equal(t, true, snapshot.PlanDryRun.Result["dry_run"])

	result.Result["dry_run"] = false
	assert.Equal(t, true, instance.Snapshot().PlanDryRun.Result["dry_run"])
	_, err = processor.DryRun(context.Background())
	require.NoError(t, err)
	assert.Len(t, service.requests, 1)
}

func TestPlanProcessorDryRunFailureRejectsPlan(t *testing.T) {
	instance := plannedWorkflow(t, "incident-001", map[string]any{
		"tool_id": "generate_image", "target_version": "mapping-v1",
		"expected_version": "mapping-v2", "idempotency_key": "rollback-001",
	})
	service := &recordingPlatform{
		operation: platform.Operation{
			ID: "operation-dry-run-rejected", Status: platform.OperationRejected,
			Message: "expected version no longer active",
		},
		err: platform.ErrPreconditionFailed,
	}
	processor, err := NewPlanProcessor(service, instance)
	require.NoError(t, err)

	result, err := processor.DryRun(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPlanDryRunFailed)
	assert.Equal(t, workflow.PlanDryRunFailed, result.Status)
	require.NotNil(t, result.Failure)
	assert.Equal(t, workflow.PlanDryRunFailurePreconditionChanged, result.Failure.Category)
	assert.Equal(t, "precondition_failed", result.Failure.Code)
	assert.Equal(t, workflow.PlanDryRunNextReinvestigate, result.Failure.NextAction)

	snapshot := instance.Snapshot()
	assert.Equal(t, workflow.StateReinvestigating, snapshot.State)
	require.NotNil(t, snapshot.PlanDryRun)
	last := snapshot.History[len(snapshot.History)-1]
	assert.Equal(t, workflow.EventPlanRejected, last.Event)
	assert.Equal(t, workflow.ActorWorkflow, last.Actor)
	assert.Equal(t, "operation-dry-run-rejected", last.Metadata["operation_id"])
	assert.Equal(t, string(platform.OperationRejected), last.Metadata["operation_status"])
	assert.Equal(t, "precondition_changed", last.Metadata["failure_category"])
	assert.Equal(t, "reinvestigate", last.Metadata["failure_next_action"])

	_, err = processor.DryRun(context.Background())
	assert.ErrorIs(t, err, ErrPlanDryRunFailed)
	assert.Len(t, service.requests, 1)
}

func TestPlanProcessorDryRunDoesNotChangeSimulatorWorld(t *testing.T) {
	item := datasetCase(t, "mapping-regression-rollback-001")
	service, err := simulator.New(item.Scenario)
	require.NoError(t, err)
	require.NoError(t, service.Advance(context.Background(), 5*time.Minute))
	before := service.Snapshot()
	require.True(t, before.Configs["mapping-v2"].Active)
	require.False(t, before.Configs["mapping-v1"].Active)

	instance := plannedWorkflow(t, item.Scenario.Metadata.ID, map[string]any{
		"tool_id": "generate_image", "target_version": "mapping-v1",
		"expected_version": "mapping-v2", "idempotency_key": "rollback-001",
	})
	processor, err := NewPlanProcessor(service, instance)
	require.NoError(t, err)

	result, err := processor.DryRun(context.Background())
	require.NoError(t, err)
	assert.Equal(t, workflow.PlanDryRunSucceeded, result.Status)
	after := service.Snapshot()
	assert.True(t, after.Configs["mapping-v2"].Active)
	assert.False(t, after.Configs["mapping-v1"].Active)
}

func TestPlanProcessorDryRunRequiresPlannedState(t *testing.T) {
	instance, err := workflow.NewIncidentWorkflow(workflow.Config{IncidentID: "incident-001"})
	require.NoError(t, err)
	service := &recordingPlatform{}
	processor, err := NewPlanProcessor(service, instance)
	require.NoError(t, err)

	_, err = processor.DryRun(context.Background())
	assert.ErrorIs(t, err, workflow.ErrInvalidTransition)
	assert.Empty(t, service.requests)
}

func plannedWorkflow(t *testing.T, incidentID string, arguments map[string]any) *workflow.IncidentWorkflow {
	t.Helper()
	instance, err := workflow.NewIncidentWorkflow(workflow.Config{IncidentID: incidentID})
	require.NoError(t, err)
	_, err = instance.Apply(workflow.Event{Type: workflow.EventStartInvestigation, Actor: workflow.ActorController})
	require.NoError(t, err)
	_, err = instance.SubmitPlan(workflow.PlanDraft{
		Summary: "rollback unhealthy mapping", RootCause: "mapping regression",
		EvidenceRefs: []string{"change:mapping-v2", "log:invalid_parameter_type"},
		Remediation:  workflow.PlannedAction{ToolName: "rollback_mapping", Arguments: arguments},
		ProbeRouteID: "route-a", RecoveryPolicyID: "default-safe-recovery",
	})
	require.NoError(t, err)
	return instance
}

func datasetCase(t *testing.T, id string) scenario.Case {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	dataset, err := scenario.LoadDataset(filepath.Join(filepath.Dir(file), "..", "..", "data", "toolops-v1"))
	require.NoError(t, err)
	for _, item := range dataset.Cases {
		if item.Scenario.Metadata.ID == id {
			return item
		}
	}
	require.FailNow(t, "scenario not found", id)
	return scenario.Case{}
}

func TestPlanProcessorReturnsFailureForTerminalOperationWithoutCallError(t *testing.T) {
	instance := plannedWorkflow(t, "incident-001", map[string]any{
		"tool_id": "generate_image", "target_version": "mapping-v1", "idempotency_key": "rollback-001",
	})
	service := &recordingPlatform{operation: platform.Operation{ID: "operation-failed", Status: platform.OperationFailed}}
	processor, err := NewPlanProcessor(service, instance)
	require.NoError(t, err)

	result, err := processor.DryRun(context.Background())
	assert.ErrorIs(t, err, ErrPlanDryRunFailed)
	assert.Equal(t, workflow.PlanDryRunFailed, result.Status)
	require.NotNil(t, result.Failure)
	assert.Equal(t, workflow.PlanDryRunFailureExecutionFailed, result.Failure.Category)
	assert.Equal(t, workflow.StateReinvestigating, instance.Snapshot().State)
}

func TestPlanProcessorDryRunWrapsPlatformFailure(t *testing.T) {
	instance := plannedWorkflow(t, "incident-001", map[string]any{
		"tool_id": "generate_image", "target_version": "mapping-v1", "idempotency_key": "rollback-001",
	})
	platformErr := errors.New("platform unavailable")
	service := &recordingPlatform{err: platformErr}
	processor, err := NewPlanProcessor(service, instance)
	require.NoError(t, err)

	result, err := processor.DryRun(context.Background())
	assert.ErrorIs(t, err, platformErr)
	assert.ErrorContains(t, err, "platform unavailable")
	assert.Equal(t, workflow.PlanDryRunIndeterminate, result.Status)
	require.NotNil(t, result.Failure)
	assert.Equal(t, workflow.PlanDryRunFailurePlatformUnavailable, result.Failure.Category)
	assert.Equal(t, workflow.PlanDryRunNextRetry, result.Failure.NextAction)
	assert.True(t, result.Failure.Retryable)
	assert.Equal(t, workflow.StatePlanned, instance.Snapshot().State)

	_, err = processor.DryRun(context.Background())
	assert.ErrorIs(t, err, platformErr)
	assert.Len(t, service.requests, 2)
}

func TestAnalyzeDryRunResultClassifiesNextAction(t *testing.T) {
	tests := []struct {
		name       string
		operation  platform.Operation
		err        error
		status     workflow.PlanDryRunStatus
		category   workflow.PlanDryRunFailureCategory
		code       string
		nextAction workflow.PlanDryRunNextAction
		retryable  bool
	}{
		{
			name: "missing capability requires replanning", err: platform.ErrNotFound,
			status: workflow.PlanDryRunFailed, category: workflow.PlanDryRunFailurePlanInvalid,
			code: "capability_not_found", nextAction: workflow.PlanDryRunNextReplan,
		},
		{
			name: "unsupported capability requires replanning", err: platform.ErrUnsupported,
			status: workflow.PlanDryRunFailed, category: workflow.PlanDryRunFailurePlanInvalid,
			code: "capability_unsupported", nextAction: workflow.PlanDryRunNextReplan,
		},
		{
			name: "authorization failure escalates", err: platform.ErrUnauthorized,
			status: workflow.PlanDryRunFailed, category: workflow.PlanDryRunFailureAuthorizationRequired,
			code: "authorization_denied", nextAction: workflow.PlanDryRunNextEscalate,
		},
		{
			name: "state conflict requires evidence refresh", err: platform.ErrConflict,
			status: workflow.PlanDryRunFailed, category: workflow.PlanDryRunFailurePreconditionChanged,
			code: "state_conflict", nextAction: workflow.PlanDryRunNextReinvestigate,
		},
		{
			name: "transport failure retries", err: errors.New("connection reset"),
			status: workflow.PlanDryRunIndeterminate, category: workflow.PlanDryRunFailurePlatformUnavailable,
			code: "platform_unavailable", nextAction: workflow.PlanDryRunNextRetry, retryable: true,
		},
		{
			name:      "operation failure requires evidence refresh",
			operation: platform.Operation{Status: platform.OperationFailed, Message: "execution failed"},
			status:    workflow.PlanDryRunFailed, category: workflow.PlanDryRunFailureExecutionFailed,
			code: "operation_failed", nextAction: workflow.PlanDryRunNextReinvestigate,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, failure := analyzeDryRunResult(test.operation, test.err)
			assert.Equal(t, test.status, status)
			require.NotNil(t, failure)
			assert.Equal(t, test.category, failure.Category)
			assert.Equal(t, test.code, failure.Code)
			assert.Equal(t, test.nextAction, failure.NextAction)
			assert.Equal(t, test.retryable, failure.Retryable)
		})
	}
}

func TestPlanProcessorDryRunAuthorizationFailureEscalates(t *testing.T) {
	instance := plannedWorkflow(t, "incident-001", map[string]any{
		"tool_id": "generate_image", "target_version": "mapping-v1", "idempotency_key": "rollback-001",
	})
	service := &recordingPlatform{
		operation: platform.Operation{ID: "operation-unauthorized", Status: platform.OperationRejected},
		err:       platform.ErrUnauthorized,
	}
	processor, err := NewPlanProcessor(service, instance)
	require.NoError(t, err)

	result, err := processor.DryRun(context.Background())
	assert.ErrorIs(t, err, ErrPlanDryRunFailed)
	require.NotNil(t, result.Failure)
	assert.Equal(t, workflow.PlanDryRunNextEscalate, result.Failure.NextAction)
	assert.Equal(t, workflow.StateEscalated, instance.Snapshot().State)
}
