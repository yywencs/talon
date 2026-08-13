package controller

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/execution"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/storage"
	"github.com/wen/opentalon/internal/workflow"
)

type idempotentExecutionPlatform struct {
	platform.ToolOpsPlatform
	mu               sync.Mutex
	capabilities     []platform.RemediationCapability
	operations       map[string]platform.Operation
	executeCalls     int
	sideEffects      int
	failFirstCall    bool
	async            bool
	stayPending      bool
	blockSubmit      bool
	executionStatus  platform.OperationStatus
	executionTools   []string
	probeOutcome     string
	probeAsync       bool
	probeStayPending bool
	probeCalls       int
}

func (p *idempotentExecutionPlatform) GetRemediationCapabilities(context.Context, platform.StateQuery) ([]platform.RemediationCapability, error) {
	return append([]platform.RemediationCapability(nil), p.capabilities...), nil
}

func (p *idempotentExecutionPlatform) ExecuteRemediation(ctx context.Context, request platform.RemediationRequest) (platform.Operation, error) {
	if p.blockSubmit && !request.DryRun {
		<-ctx.Done()
		return platform.Operation{}, ctx.Err()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if request.DryRun {
		return platform.Operation{
			ID: "dry-run-" + request.ToolName, IncidentID: request.IncidentID,
			Kind: platform.OperationRemediation, Name: request.ToolName,
			Status: platform.OperationSucceeded, IdempotencyKey: request.IdempotencyKey,
		}, nil
	}
	p.executeCalls++
	if existing, ok := p.operations[request.IdempotencyKey]; ok {
		return existing, nil
	}
	p.sideEffects++
	p.executionTools = append(p.executionTools, request.ToolName)
	status := platform.OperationSucceeded
	if p.executionStatus != "" {
		status = p.executionStatus
	}
	if p.async {
		status = platform.OperationPending
	}
	operation := platform.Operation{
		ID: "execute-" + request.ToolName, IncidentID: request.IncidentID,
		Kind: platform.OperationRemediation, Name: request.ToolName,
		Status: status, IdempotencyKey: request.IdempotencyKey,
	}
	p.operations[request.IdempotencyKey] = operation
	if p.failFirstCall && p.executeCalls == 1 {
		return platform.Operation{}, errors.New("response lost after platform accepted operation")
	}
	return operation, nil
}

func (p *idempotentExecutionPlatform) RequestProbe(_ context.Context, request platform.ProbeRequest) (platform.Operation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.probeCalls++
	if existing, ok := p.operations[request.IdempotencyKey]; ok {
		return existing, nil
	}
	outcome := p.probeOutcome
	if outcome == "" {
		outcome = "healthy"
	}
	status := platform.OperationSucceeded
	result := map[string]any{"outcome": outcome}
	if p.probeAsync {
		status = platform.OperationPending
		result = map[string]any{"outcome": "pending"}
	}
	operation := platform.Operation{
		ID: "probe-" + request.RouteID, IncidentID: request.IncidentID,
		Kind: platform.OperationProbe, Name: "request_probe", Status: status,
		IdempotencyKey: request.IdempotencyKey, Result: result,
	}
	p.operations[request.IdempotencyKey] = operation
	return operation, nil
}

func (p *idempotentExecutionPlatform) GetOperation(_ context.Context, query platform.OperationQuery) (platform.Operation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, operation := range p.operations {
		if operation.ID == query.OperationID {
			if operation.Kind == platform.OperationRemediation && p.async && !p.stayPending && operation.Status == platform.OperationPending {
				operation.Status = platform.OperationSucceeded
				p.operations[key] = operation
			}
			if operation.Kind == platform.OperationProbe && p.probeAsync && !p.probeStayPending && operation.Status == platform.OperationPending {
				outcome := p.probeOutcome
				if outcome == "" {
					outcome = "healthy"
				}
				operation.Status = platform.OperationSucceeded
				operation.Result = map[string]any{"outcome": outcome}
				p.operations[key] = operation
			}
			return operation, nil
		}
	}
	return platform.Operation{}, platform.ErrNotFound
}

func TestActionExecutorRunsPlanActionsStrictlyInOrder(t *testing.T) {
	service := executionPlatform("first_fix", "second_fix")
	processor, instance, database := remediatingProcessor(t, service, "worker-a", time.Minute, []workflow.PlannedAction{
		{ToolName: "first_fix", Arguments: map[string]any{"idempotency_key": "agent-first"}},
		{ToolName: "second_fix", Arguments: map[string]any{"idempotency_key": "agent-second"}},
	})
	defer database.Close()

	first, err := processor.ExecuteNext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, execution.StatusSucceeded, first.Status)
	assert.Equal(t, 1, first.Sequence)
	assert.Equal(t, workflow.StateRemediating, instance.Snapshot().State)

	second, err := processor.ExecuteNext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, execution.StatusSucceeded, second.Status)
	assert.Equal(t, 2, second.Sequence)
	assert.Equal(t, workflow.StateProbing, instance.Snapshot().State)
	assert.Equal(t, []string{"first_fix", "second_fix"}, service.executionTools)
	assert.Equal(t, 2, service.sideEffects)
}

func TestActionExecutorReconcilesPendingOperationWithoutResubmission(t *testing.T) {
	service := executionPlatform("async_fix")
	service.async = true
	processor, instance, database := remediatingProcessor(t, service, "worker-a", time.Minute, []workflow.PlannedAction{
		{ToolName: "async_fix", Arguments: map[string]any{"idempotency_key": "agent-async"}},
	})
	defer database.Close()

	running, err := processor.ExecuteNext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, execution.StatusRunning, running.Status)
	assert.NotEmpty(t, running.OperationID)
	assert.Equal(t, workflow.StateRemediating, instance.Snapshot().State)

	time.Sleep(2 * time.Millisecond)
	finished, err := processor.ExecuteNext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, execution.StatusSucceeded, finished.Status)
	assert.Equal(t, workflow.StateProbing, instance.Snapshot().State)
	assert.Equal(t, 1, service.executeCalls)
	assert.Equal(t, 1, service.sideEffects)
}

func TestActionExecutorLeaseTakeoverRetriesRequestButSideEffectOccursOnce(t *testing.T) {
	service := executionPlatform("safe_fix")
	service.failFirstCall = true
	processorA, instance, database := remediatingProcessor(t, service, "worker-a", 5*time.Millisecond, []workflow.PlannedAction{
		{ToolName: "safe_fix", Arguments: map[string]any{"idempotency_key": "agent-key-is-ignored"}},
	})
	defer database.Close()

	unknown, err := processorA.ExecuteNext(context.Background())
	assert.ErrorIs(t, err, ErrActionExecutionUnknown)
	assert.Equal(t, execution.StatusUnknown, unknown.Status)
	time.Sleep(10 * time.Millisecond)
	processorB, err := NewPlanProcessor(service, instance,
		WithApprovalStore(database.Approvals()),
		WithExecutionStore(database.Executions(), "worker-b", time.Minute),
		WithAsyncExecution(fastAsyncExecutionConfig()))
	require.NoError(t, err)

	finished, err := processorB.ExecuteNext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, execution.StatusSucceeded, finished.Status)
	assert.Equal(t, workflow.StateProbing, instance.Snapshot().State)
	assert.Equal(t, 2, service.executeCalls, "请求因不确定结果被重试")
	assert.Equal(t, 1, service.sideEffects, "稳定幂等键保证副作用只发生一次")
	assert.Equal(t, finished.ActionID+":execute", finished.IdempotencyKey)
}

func TestActionWorkerPollsOperationUntilSucceeded(t *testing.T) {
	service := executionPlatform("async_fix")
	service.async = true
	processor, instance, database := remediatingProcessor(t, service, "worker-a", time.Second, []workflow.PlannedAction{
		{ToolName: "async_fix", Arguments: map[string]any{}},
	})
	defer database.Close()
	worker, err := NewActionWorker(processor, time.Millisecond)
	require.NoError(t, err)

	require.NoError(t, worker.Run(context.Background()))
	assert.Equal(t, workflow.StateProbing, instance.Snapshot().State)
	assert.Equal(t, 1, service.executeCalls)
	assert.Equal(t, 1, service.sideEffects)
}

func TestActionWorkerFailsOperationAfterDeadline(t *testing.T) {
	service := executionPlatform("stuck_fix")
	service.async = true
	service.stayPending = true
	processor, instance, database := remediatingProcessor(t, service, "worker-a", time.Second, []workflow.PlannedAction{
		{ToolName: "stuck_fix", Arguments: map[string]any{}},
	})
	defer database.Close()
	processor.operationTimeout = 5 * time.Millisecond
	worker, err := NewActionWorker(processor, time.Millisecond)
	require.NoError(t, err)

	err = worker.Run(context.Background())
	assert.ErrorIs(t, err, ErrOperationTimedOut)
	assert.Equal(t, workflow.StateReinvestigating, instance.Snapshot().State)
	records, listErr := database.Executions().ListPlan(context.Background(), instance.Snapshot().Plan.ID)
	require.NoError(t, listErr)
	require.Len(t, records, 1)
	assert.Equal(t, execution.StatusFailed, records[0].Status)
	assert.Equal(t, "timed_out", records[0].OperationStatus)
}

func TestActionExecutorSubmissionTimeoutBecomesRetryableUnknown(t *testing.T) {
	service := executionPlatform("slow_submit")
	processor, instance, database := remediatingProcessor(t, service, "worker-a", time.Second, []workflow.PlannedAction{
		{ToolName: "slow_submit", Arguments: map[string]any{}},
	})
	defer database.Close()
	processor.submitTimeout = 2 * time.Millisecond
	service.blockSubmit = true

	record, err := processor.ExecuteNext(context.Background())
	assert.ErrorIs(t, err, ErrActionExecutionUnknown)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, execution.StatusUnknown, record.Status)
	assert.NotNil(t, record.NextPollAt)
	assert.Empty(t, record.OwnerID)
	assert.Equal(t, workflow.StateRemediating, instance.Snapshot().State)
}

func executionPlatform(tools ...string) *idempotentExecutionPlatform {
	capabilities := make([]platform.RemediationCapability, len(tools))
	for index, tool := range tools {
		capabilities[index] = platform.RemediationCapability{Name: tool, Risk: "low"}
	}
	return &idempotentExecutionPlatform{capabilities: capabilities, operations: make(map[string]platform.Operation)}
}

func remediatingProcessor(t *testing.T, service *idempotentExecutionPlatform, workerID string, lease time.Duration, actions []workflow.PlannedAction) (*PlanProcessor, *workflow.IncidentWorkflow, *storage.Storage) {
	t.Helper()
	ctx := context.Background()
	instance, err := workflow.NewIncidentWorkflow(workflow.Config{IncidentID: "execution-incident"})
	require.NoError(t, err)
	_, err = instance.Apply(workflow.Event{Type: workflow.EventStartInvestigation, Actor: workflow.ActorController})
	require.NoError(t, err)
	_, err = instance.SubmitPlan(workflow.PlanDraft{
		Summary: "execute fixes", RootCause: "confirmed failure", EvidenceRefs: []string{"trace:execution"},
		Actions: actions, ProbeRouteID: "route-a", RecoveryPolicyID: "safe-recovery",
	})
	require.NoError(t, err)
	database, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "talon.db"))
	require.NoError(t, err)
	processor, err := NewPlanProcessor(service, instance,
		WithApprovalStore(database.Approvals()),
		WithExecutionStore(database.Executions(), workerID, lease),
		WithAsyncExecution(fastAsyncExecutionConfig()))
	require.NoError(t, err)
	_, err = processor.DryRun(ctx)
	require.NoError(t, err)
	_, err = processor.EvaluatePolicy(ctx)
	require.NoError(t, err)
	require.Equal(t, workflow.StateRemediating, instance.Snapshot().State)
	return processor, instance, database
}

func fastAsyncExecutionConfig() AsyncExecutionConfig {
	return AsyncExecutionConfig{
		SubmitTimeout: time.Second, InitialPollInterval: time.Millisecond,
		MaxPollInterval: 4 * time.Millisecond, OperationTimeout: time.Minute,
	}
}
