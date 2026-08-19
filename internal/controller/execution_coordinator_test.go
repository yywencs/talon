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
	requests          []platform.RemediationRequest
	operation         platform.Operation
	err               error
	capabilities      []platform.RemediationCapability
	capabilitiesErr   error
	capabilitiesCalls int
}

func (p *recordingPlatform) ExecuteRemediation(_ context.Context, request platform.RemediationRequest) (platform.Operation, error) {
	p.requests = append(p.requests, request)
	return p.operation, p.err
}

func (p *recordingPlatform) GetRemediationCapabilities(_ context.Context, _ platform.StateQuery) ([]platform.RemediationCapability, error) {
	p.capabilitiesCalls++
	return append([]platform.RemediationCapability(nil), p.capabilities...), p.capabilitiesErr
}

func TestExecutionCoordinatorDryRunRecordsSuccessAndIsIdempotent(t *testing.T) {
	instance := validatingWorkflow(t, "incident-001", map[string]any{
		"tool_id": "generate_image", "target_version": "mapping-v1",
		"expected_version": "mapping-v2", "idempotency_key": "rollback-001",
		"dry_run": false,
	})
	service := &recordingPlatform{operation: platform.Operation{
		ID: "operation-dry-run-001", Status: platform.OperationSucceeded,
		Message: "dry run completed", Result: map[string]any{"dry_run": true},
	}}
	processor, err := NewExecutionCoordinator(service, instance)
	require.NoError(t, err)

	result, err := processor.DryRun(context.Background())
	require.NoError(t, err)
	require.Len(t, service.requests, 1)
	request := service.requests[0]
	assert.True(t, request.DryRun)
	assert.Equal(t, "incident-001-intent-2-action-1:dry-run", request.IdempotencyKey)
	assert.Equal(t, "mapping-v2", request.ExpectedVersion)
	assert.Equal(t, map[string]any{"tool_id": "generate_image", "target_version": "mapping-v1"}, request.Arguments)
	require.Len(t, result, 1)
	assert.Equal(t, workflow.ActionDryRunSucceeded, result[0].Status)

	snapshot := instance.Snapshot()
	assert.Equal(t, workflow.StateValidating, snapshot.State)
	require.Len(t, snapshot.ActionDryRuns, 1)
	assert.Equal(t, "operation-dry-run-001", snapshot.ActionDryRuns[0].OperationID)
	assert.Equal(t, true, snapshot.ActionDryRuns[0].Result["dry_run"])

	result[0].Result["dry_run"] = false
	assert.Equal(t, true, instance.Snapshot().ActionDryRuns[0].Result["dry_run"])
	_, err = processor.DryRun(context.Background())
	require.NoError(t, err)
	assert.Len(t, service.requests, 1)
}

func TestExecutionCoordinatorDryRunFailureRejectsIntent(t *testing.T) {
	instance := validatingWorkflow(t, "incident-001", map[string]any{
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
	processor, err := NewExecutionCoordinator(service, instance)
	require.NoError(t, err)

	result, err := processor.DryRun(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrActionDryRunFailed)
	require.Len(t, result, 1)
	assert.Equal(t, workflow.ActionDryRunFailed, result[0].Status)
	require.NotNil(t, result[0].Failure)
	assert.Equal(t, workflow.ActionDryRunFailurePreconditionChanged, result[0].Failure.Category)
	assert.Equal(t, "precondition_failed", result[0].Failure.Code)
	assert.Equal(t, workflow.ActionDryRunNextNeedsAgent, result[0].Failure.NextAction)

	snapshot := instance.Snapshot()
	assert.Equal(t, workflow.StateInvestigating, snapshot.State)
	require.Len(t, snapshot.ActionDryRuns, 1)
	last := snapshot.History[len(snapshot.History)-1]
	assert.Equal(t, workflow.EventCheckpointNeedsAgent, last.Event)
	assert.Equal(t, workflow.ActorWorkflow, last.Actor)
	require.NotNil(t, last.Failure)
	assert.Equal(t, "operation-dry-run-rejected", last.Failure.OperationID)
	assert.Equal(t, string(platform.OperationRejected), last.Failure.OperationStatus)
	assert.Equal(t, workflow.FailureCategoryPreconditionChanged, last.Failure.Category)
	assert.Equal(t, workflow.FailureNextNeedsAgent, last.Failure.NextAction)

	_, err = processor.DryRun(context.Background())
	assert.ErrorIs(t, err, ErrActionDryRunFailed)
	assert.Len(t, service.requests, 1)
}

func TestExecutionCoordinatorDryRunDoesNotChangeSimulatorWorld(t *testing.T) {
	item := datasetCase(t, "mapping-regression-rollback-001")
	service, err := simulator.New(item.Scenario)
	require.NoError(t, err)
	require.NoError(t, service.Advance(context.Background(), 5*time.Minute))
	before := service.Snapshot()
	require.True(t, before.Configs["mapping-v2"].Active)
	require.False(t, before.Configs["mapping-v1"].Active)

	instance := validatingWorkflow(t, item.Scenario.Metadata.ID, map[string]any{
		"tool_id": "generate_image", "target_version": "mapping-v1",
		"expected_version": "mapping-v2", "idempotency_key": "rollback-001",
	})
	processor, err := NewExecutionCoordinator(service, instance)
	require.NoError(t, err)

	result, err := processor.DryRun(context.Background())
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, workflow.ActionDryRunSucceeded, result[0].Status)
	after := service.Snapshot()
	assert.True(t, after.Configs["mapping-v2"].Active)
	assert.False(t, after.Configs["mapping-v1"].Active)
}

func TestExecutionCoordinatorDryRunRequiresValidatingState(t *testing.T) {
	instance, err := workflow.NewIncidentWorkflow(workflow.Config{IncidentID: "incident-001"})
	require.NoError(t, err)
	service := &recordingPlatform{}
	processor, err := NewExecutionCoordinator(service, instance)
	require.NoError(t, err)

	_, err = processor.DryRun(context.Background())
	assert.ErrorIs(t, err, workflow.ErrInvalidTransition)
	assert.Empty(t, service.requests)
}

func validatingWorkflow(t *testing.T, incidentID string, arguments map[string]any) *workflow.IncidentWorkflow {
	t.Helper()
	instance, err := workflow.NewIncidentWorkflow(workflow.Config{IncidentID: incidentID})
	require.NoError(t, err)
	_, err = instance.Apply(workflow.Event{Type: workflow.EventStartInvestigation, Actor: workflow.ActorController})
	require.NoError(t, err)
	_, err = instance.SubmitExecutionIntent(workflow.ExecutionIntentDraft{
		Summary: "rollback unhealthy mapping", RootCause: "mapping regression",
		EvidenceRefs: []string{"change:mapping-v2", "log:invalid_parameter_type"},
		Stages: []workflow.ExecutionStageDraft{{StageID: "rollback", Goal: "rollback mapping",
			Actions:          []workflow.IntendedAction{{Key: "rollback-mapping", ToolName: "rollback_mapping", Arguments: arguments}},
			CheckpointPolicy: workflow.CheckpointPolicy{DefaultDecision: workflow.CheckpointSucceeded}}},
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

func TestExecutionCoordinatorReturnsFailureForTerminalOperationWithoutCallError(t *testing.T) {
	instance := validatingWorkflow(t, "incident-001", map[string]any{
		"tool_id": "generate_image", "target_version": "mapping-v1", "idempotency_key": "rollback-001",
	})
	service := &recordingPlatform{operation: platform.Operation{ID: "operation-failed", Status: platform.OperationFailed}}
	processor, err := NewExecutionCoordinator(service, instance)
	require.NoError(t, err)

	result, err := processor.DryRun(context.Background())
	assert.ErrorIs(t, err, ErrActionDryRunFailed)
	require.Len(t, result, 1)
	assert.Equal(t, workflow.ActionDryRunFailed, result[0].Status)
	require.NotNil(t, result[0].Failure)
	assert.Equal(t, workflow.ActionDryRunFailureExecutionFailed, result[0].Failure.Category)
	assert.Equal(t, workflow.StateInvestigating, instance.Snapshot().State)
}

func TestExecutionCoordinatorDryRunWrapsPlatformFailure(t *testing.T) {
	instance := validatingWorkflow(t, "incident-001", map[string]any{
		"tool_id": "generate_image", "target_version": "mapping-v1", "idempotency_key": "rollback-001",
	})
	platformErr := context.DeadlineExceeded
	service := &recordingPlatform{err: platformErr}
	processor, err := NewExecutionCoordinator(service, instance)
	require.NoError(t, err)

	result, err := processor.DryRun(context.Background())
	assert.ErrorIs(t, err, platformErr)
	assert.ErrorContains(t, err, "context deadline exceeded")
	require.Len(t, result, 1)
	assert.Equal(t, workflow.ActionDryRunIndeterminate, result[0].Status)
	require.NotNil(t, result[0].Failure)
	assert.Equal(t, workflow.ActionDryRunFailurePlatformUnavailable, result[0].Failure.Category)
	assert.Equal(t, workflow.ActionDryRunNextRetry, result[0].Failure.NextAction)
	assert.True(t, result[0].Failure.Retryable)
	assert.Equal(t, workflow.StateValidating, instance.Snapshot().State)

	_, err = processor.DryRun(context.Background())
	assert.ErrorIs(t, err, platformErr)
	assert.Len(t, service.requests, 2)
}

func TestAnalyzeDryRunResultClassifiesNextAction(t *testing.T) {
	tests := []struct {
		name       string
		operation  platform.Operation
		err        error
		status     workflow.ActionDryRunStatus
		category   workflow.ActionDryRunFailureCategory
		code       string
		nextAction workflow.ActionDryRunNextAction
		retryable  bool
	}{
		{
			name: "missing capability needs agent", err: platform.ErrNotFound,
			status: workflow.ActionDryRunFailed, category: workflow.ActionDryRunFailureIntentInvalid,
			code: "capability_not_found", nextAction: workflow.ActionDryRunNextNeedsAgent,
		},
		{
			name: "unsupported capability needs agent", err: platform.ErrUnsupported,
			status: workflow.ActionDryRunFailed, category: workflow.ActionDryRunFailureIntentInvalid,
			code: "capability_unsupported", nextAction: workflow.ActionDryRunNextNeedsAgent,
		},
		{
			name: "authorization failure escalates", err: platform.ErrUnauthorized,
			status: workflow.ActionDryRunFailed, category: workflow.ActionDryRunFailureAuthorizationRequired,
			code: "authorization_denied", nextAction: workflow.ActionDryRunNextEscalate,
		},
		{
			name: "state conflict requires evidence refresh", err: platform.ErrConflict,
			status: workflow.ActionDryRunFailed, category: workflow.ActionDryRunFailurePreconditionChanged,
			code: "state_conflict", nextAction: workflow.ActionDryRunNextNeedsAgent,
		},
		{
			name: "typed timeout retries", err: context.DeadlineExceeded,
			status: workflow.ActionDryRunIndeterminate, category: workflow.ActionDryRunFailurePlatformUnavailable,
			code: "platform_unavailable", nextAction: workflow.ActionDryRunNextRetry, retryable: true,
		},
		{
			name: "unknown error fails closed", err: errors.New("new provider error"),
			status: workflow.ActionDryRunFailed, category: workflow.ActionDryRunFailureUnclassified,
			code: "unclassified_dry_run_error", nextAction: workflow.ActionDryRunNextEscalate,
		},
		{
			name:      "unknown operation status escalates",
			operation: platform.Operation{Status: platform.OperationStatus("future_status")},
			status:    workflow.ActionDryRunFailed, category: workflow.ActionDryRunFailureInvalidResponse,
			code: "invalid_operation_status", nextAction: workflow.ActionDryRunNextEscalate,
		},
		{
			name:      "operation failure requires evidence refresh",
			operation: platform.Operation{Status: platform.OperationFailed, Message: "execution failed"},
			status:    workflow.ActionDryRunFailed, category: workflow.ActionDryRunFailureExecutionFailed,
			code: "operation_failed", nextAction: workflow.ActionDryRunNextNeedsAgent,
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

func TestExecutionCoordinatorDryRunAuthorizationFailureEscalates(t *testing.T) {
	instance := validatingWorkflow(t, "incident-001", map[string]any{
		"tool_id": "generate_image", "target_version": "mapping-v1", "idempotency_key": "rollback-001",
	})
	service := &recordingPlatform{
		operation: platform.Operation{ID: "operation-unauthorized", Status: platform.OperationRejected},
		err:       platform.ErrUnauthorized,
	}
	processor, err := NewExecutionCoordinator(service, instance)
	require.NoError(t, err)

	result, err := processor.DryRun(context.Background())
	assert.ErrorIs(t, err, ErrActionDryRunFailed)
	require.Len(t, result, 1)
	require.NotNil(t, result[0].Failure)
	assert.Equal(t, workflow.ActionDryRunNextEscalate, result[0].Failure.NextAction)
	assert.Equal(t, workflow.StateBlocked, instance.Snapshot().State)
}

func TestPersistCheckpointSurvivesCanceledRunContext(t *testing.T) {
	instance, err := workflow.NewIncidentWorkflow(workflow.Config{IncidentID: "persist-after-cancel"})
	require.NoError(t, err)
	called := false
	processor, err := NewExecutionCoordinator(&recordingPlatform{}, instance, WithWorkflowCheckpoint(func(ctx context.Context, _ workflow.Snapshot) error {
		called = true
		return ctx.Err()
	}))
	require.NoError(t, err)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, processor.persistCheckpoint(canceled))
	assert.True(t, called)
}
