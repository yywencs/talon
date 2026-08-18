// Package controller 编排 Workflow 与受管平台之间的确定性执行步骤。
package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wen/opentalon/internal/approval"
	"github.com/wen/opentalon/internal/execution"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/workflow"
)

var ErrPlanDryRunFailed = errors.New("plan dry run failed")

// PlanProcessor 负责执行已经由 Workflow 冻结的 Plan，不让 Agent 直接调用生产写操作。
type PlanProcessor struct {
	platform         platform.ToolOpsPlatform
	workflow         *workflow.IncidentWorkflow
	approvalStore    approval.Store
	executionStore   execution.Store
	workerID         string
	leaseDuration    time.Duration
	submitTimeout    time.Duration
	pollInitial      time.Duration
	pollMaximum      time.Duration
	operationTimeout time.Duration
	checkpoint       func(context.Context, workflow.Snapshot) error
}

// AsyncExecutionConfig 配置异步修复提交、轮询退避和 Operation 总超时。
type AsyncExecutionConfig struct {
	SubmitTimeout       time.Duration
	InitialPollInterval time.Duration
	MaxPollInterval     time.Duration
	OperationTimeout    time.Duration
}

// PlanProcessorOption 配置 PlanProcessor 的可选控制面能力。
type PlanProcessorOption func(*PlanProcessor) error

// WithApprovalStore 接入持久化审批收件箱。
func WithApprovalStore(store approval.Store) PlanProcessorOption {
	return func(processor *PlanProcessor) error {
		if store == nil {
			return fmt.Errorf("approval store is required")
		}
		processor.approvalStore = store
		return nil
	}
}

// WithExecutionStore 接入 Action 执行记录、Worker 身份和租约时长。
func WithExecutionStore(store execution.Store, workerID string, leaseDuration time.Duration) PlanProcessorOption {
	return func(processor *PlanProcessor) error {
		if store == nil {
			return fmt.Errorf("execution store is required")
		}
		if strings.TrimSpace(workerID) == "" {
			return fmt.Errorf("execution worker ID is required")
		}
		if leaseDuration <= 0 {
			return fmt.Errorf("execution lease duration must be positive")
		}
		processor.executionStore = store
		processor.workerID = strings.TrimSpace(workerID)
		processor.leaseDuration = leaseDuration
		return nil
	}
}

// WithAsyncExecution 配置“短同步提交 + 异步轮询”的时间边界。
func WithAsyncExecution(config AsyncExecutionConfig) PlanProcessorOption {
	return func(processor *PlanProcessor) error {
		if config.SubmitTimeout <= 0 || config.InitialPollInterval <= 0 ||
			config.MaxPollInterval < config.InitialPollInterval || config.OperationTimeout <= 0 {
			return fmt.Errorf("async execution durations are invalid")
		}
		processor.submitTimeout = config.SubmitTimeout
		processor.pollInitial = config.InitialPollInterval
		processor.pollMaximum = config.MaxPollInterval
		processor.operationTimeout = config.OperationTimeout
		return nil
	}
}

// WithWorkflowCheckpoint 在每个确定性执行检查点持久化 Workflow/RunArtifact。
func WithWorkflowCheckpoint(checkpoint func(context.Context, workflow.Snapshot) error) PlanProcessorOption {
	return func(processor *PlanProcessor) error {
		if checkpoint == nil {
			return fmt.Errorf("workflow checkpoint callback is required")
		}
		processor.checkpoint = checkpoint
		return nil
	}
}

func (p *PlanProcessor) persistCheckpoint(ctx context.Context) error {
	if p == nil || p.checkpoint == nil {
		return nil
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return p.checkpoint(persistCtx, p.workflow.Snapshot())
}

// NewPlanProcessor 创建 Plan 执行编排器。
func NewPlanProcessor(service platform.ToolOpsPlatform, instance *workflow.IncidentWorkflow, options ...PlanProcessorOption) (*PlanProcessor, error) {
	if service == nil {
		return nil, fmt.Errorf("toolops platform is required")
	}
	if instance == nil {
		return nil, fmt.Errorf("incident workflow is required")
	}
	processor := &PlanProcessor{
		platform: service, workflow: instance,
		submitTimeout: 10 * time.Second, pollInitial: 2 * time.Second,
		pollMaximum: 30 * time.Second, operationTimeout: 10 * time.Minute,
	}
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(processor); err != nil {
			return nil, fmt.Errorf("configure plan processor: %w", err)
		}
	}
	return processor, nil
}

// DryRun 按顺序对当前冻结 Plan 的每个 Action 做无副作用预执行。
// 每个结果分别绑定 Action ID 和摘要；已完成的 Action 不会被重复调用。
func (p *PlanProcessor) DryRun(ctx context.Context) ([]workflow.PlanDryRun, error) {
	if p == nil || p.platform == nil || p.workflow == nil {
		return nil, fmt.Errorf("plan processor is not initialized")
	}
	if _, err := p.workflow.ResolveCurrentStage(); err != nil {
		return p.workflow.Snapshot().PlanDryRuns, errors.Join(ErrPlanDryRunFailed, err)
	}
	snapshot := p.workflow.Snapshot()
	for _, result := range snapshot.PlanDryRuns {
		if result.Status == workflow.PlanDryRunFailed {
			return snapshot.PlanDryRuns, ErrPlanDryRunFailed
		}
	}
	if snapshot.State != workflow.StatePlanned {
		return nil, fmt.Errorf("%w: plan dry run is not allowed in state %q", workflow.ErrInvalidTransition, snapshot.State)
	}
	if snapshot.Plan == nil {
		return nil, fmt.Errorf("planned workflow has no frozen plan")
	}

	actions := workflow.ExecutableActions(snapshot)
	if len(actions) == 0 {
		return nil, fmt.Errorf("planned workflow has no resolved actions")
	}
	for _, action := range actions {
		if terminalActionDryRun(snapshot.PlanDryRuns, action.ID) {
			continue
		}
		operation, idempotencyKey, callErr := p.dryRunAction(ctx, snapshot.IncidentID, action)
		status, failure := analyzeDryRunResult(operation, callErr)
		result := workflow.PlanDryRun{
			PlanID: snapshot.Plan.ID, ActionID: action.ID, ActionDigest: action.Digest,
			OperationID: operation.ID, IdempotencyKey: idempotencyKey,
			Status: status, OperationStatus: string(operation.Status), Message: operation.Message,
			Failure: failure, Result: cloneMap(operation.Result),
		}
		recorded, recordErr := p.workflow.RecordPlanDryRun(result)
		if recordErr != nil {
			return p.workflow.Snapshot().PlanDryRuns, fmt.Errorf("record action %q dry run: %w", action.ID, recordErr)
		}
		if persistErr := p.persistCheckpoint(ctx); persistErr != nil {
			return p.workflow.Snapshot().PlanDryRuns, fmt.Errorf("persist action %q dry run checkpoint: %w", action.ID, persistErr)
		}
		if callErr != nil {
			if recorded.Status == workflow.PlanDryRunFailed {
				return p.workflow.Snapshot().PlanDryRuns, errors.Join(ErrPlanDryRunFailed, callErr)
			}
			return p.workflow.Snapshot().PlanDryRuns, fmt.Errorf("execute action %q dry run: %w", action.ID, callErr)
		}
		if recorded.Status == workflow.PlanDryRunFailed {
			return p.workflow.Snapshot().PlanDryRuns, fmt.Errorf("%w: action %q operation status %q", ErrPlanDryRunFailed, action.ID, recorded.OperationStatus)
		}
	}
	return p.workflow.Snapshot().PlanDryRuns, nil
}

func (p *PlanProcessor) dryRunAction(ctx context.Context, incidentID string, action workflow.PlannedAction) (platform.Operation, string, error) {
	idempotencyKey := action.ID + ":dry-run"
	if action.Kind == workflow.ActionKindRemediation {
		request := remediationDryRunRequest(incidentID, action)
		operation, err := p.platform.ExecuteRemediation(ctx, request)
		return operation, request.IdempotencyKey, err
	}
	if action.Kind != workflow.ActionKindProbe && action.Kind != workflow.ActionKindRecovery {
		return platform.Operation{}, idempotencyKey, fmt.Errorf("%w: unsupported action kind %q", platform.ErrUnsupported, action.Kind)
	}
	routeID, routeOK := nonEmptyString(action.Arguments, "route_id")
	policyID, policyOK := nonEmptyString(action.Arguments, "policy_id")
	if !routeOK || !policyOK {
		return platform.Operation{}, idempotencyKey, fmt.Errorf("%w: %s requires non-empty route_id and policy_id", platform.ErrPreconditionFailed, action.ToolName)
	}
	policies, err := p.platform.GetRecoveryPolicies(ctx, platform.StateQuery{Scope: platform.Scope{IncidentID: incidentID, RouteID: routeID}})
	if err != nil {
		return platform.Operation{}, idempotencyKey, err
	}
	policyFound := false
	for _, policy := range policies {
		if strings.TrimSpace(policy.ID) == policyID {
			policyFound = true
			break
		}
	}
	if !policyFound {
		return platform.Operation{}, idempotencyKey, fmt.Errorf("%w: recovery policy %q is not available", platform.ErrNotFound, policyID)
	}
	kind := platform.OperationProbe
	if action.Kind == workflow.ActionKindRecovery {
		kind = platform.OperationRecovery
	}
	return platform.Operation{
		ID: action.ID + ":dry-run-operation", IncidentID: incidentID, Kind: kind, Name: action.ToolName,
		Status: platform.OperationSucceeded, IdempotencyKey: idempotencyKey,
		Result: map[string]any{"route_id": routeID, "policy_id": policyID, "validated": true},
	}, idempotencyKey, nil
}

func nonEmptyString(arguments map[string]any, name string) (string, bool) {
	value, ok := arguments[name].(string)
	value = strings.TrimSpace(value)
	return value, ok && value != ""
}

func remediationDryRunRequest(incidentID string, action workflow.PlannedAction) platform.RemediationRequest {
	arguments := cloneMap(action.Arguments)
	expectedVersion, _ := arguments["expected_version"].(string)
	delete(arguments, "idempotency_key")
	delete(arguments, "expected_version")
	delete(arguments, "dry_run")
	return platform.RemediationRequest{
		IncidentID: strings.TrimSpace(incidentID), ToolName: action.ToolName,
		Arguments: arguments, ExpectedVersion: strings.TrimSpace(expectedVersion), DryRun: true,
		IdempotencyKey: action.ID + ":dry-run",
	}
}

func terminalActionDryRun(values []workflow.PlanDryRun, actionID string) bool {
	for _, value := range values {
		if value.ActionID == actionID {
			return value.Status == workflow.PlanDryRunSucceeded || value.Status == workflow.PlanDryRunFailed
		}
	}
	return false
}

func analyzeDryRunResult(operation platform.Operation, err error) (workflow.PlanDryRunStatus, *workflow.PlanDryRunFailure) {
	if err != nil {
		message := strings.TrimSpace(operation.Message)
		if message == "" {
			message = err.Error()
		}
		switch {
		case errors.Is(err, platform.ErrNotFound):
			return failedPlanDryRun(workflow.PlanDryRunFailurePlanInvalid, "capability_not_found", message, workflow.PlanDryRunNextNeedsAgent)
		case errors.Is(err, platform.ErrUnsupported):
			return failedPlanDryRun(workflow.PlanDryRunFailurePlanInvalid, "capability_unsupported", message, workflow.PlanDryRunNextNeedsAgent)
		case errors.Is(err, platform.ErrUnauthorized):
			return failedPlanDryRun(workflow.PlanDryRunFailureAuthorizationRequired, "authorization_denied", message, workflow.PlanDryRunNextEscalate)
		case errors.Is(err, platform.ErrConflict):
			return failedPlanDryRun(workflow.PlanDryRunFailurePreconditionChanged, "state_conflict", message, workflow.PlanDryRunNextNeedsAgent)
		case errors.Is(err, platform.ErrPreconditionFailed):
			return failedPlanDryRun(workflow.PlanDryRunFailurePreconditionChanged, "precondition_failed", message, workflow.PlanDryRunNextNeedsAgent)
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			return workflow.PlanDryRunIndeterminate, &workflow.PlanDryRunFailure{
				Category: workflow.PlanDryRunFailurePlatformUnavailable, Code: "platform_unavailable",
				Message: message, NextAction: workflow.PlanDryRunNextRetry, Retryable: true,
			}
		default:
			return failedPlanDryRun(workflow.PlanDryRunFailureUnclassified, "unclassified_dry_run_error",
				message, workflow.PlanDryRunNextEscalate)
		}
	}
	message := strings.TrimSpace(operation.Message)
	switch operation.Status {
	case platform.OperationPending, platform.OperationRunning:
		return workflow.PlanDryRunPending, nil
	case platform.OperationSucceeded:
		return workflow.PlanDryRunSucceeded, nil
	case platform.OperationRejected:
		return failedPlanDryRun(workflow.PlanDryRunFailurePlanInvalid, "operation_rejected", message, workflow.PlanDryRunNextNeedsAgent)
	case platform.OperationFailed:
		return failedPlanDryRun(workflow.PlanDryRunFailureExecutionFailed, "operation_failed", message, workflow.PlanDryRunNextNeedsAgent)
	case platform.OperationCancelled:
		return workflow.PlanDryRunIndeterminate, &workflow.PlanDryRunFailure{
			Category: workflow.PlanDryRunFailurePlatformUnavailable, Code: "operation_cancelled",
			Message: message, NextAction: workflow.PlanDryRunNextRetry, Retryable: true,
		}
	default:
		return failedPlanDryRun(workflow.PlanDryRunFailureInvalidResponse, "invalid_operation_status",
			message, workflow.PlanDryRunNextEscalate)
	}
}

func failedPlanDryRun(category workflow.PlanDryRunFailureCategory, code, message string, next workflow.PlanDryRunNextAction) (workflow.PlanDryRunStatus, *workflow.PlanDryRunFailure) {
	return workflow.PlanDryRunFailed, &workflow.PlanDryRunFailure{
		Category: category, Code: code, Message: message, NextAction: next,
	}
}

func cloneMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = cloneValue(item)
	}
	return result
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneValue(item)
		}
		return result
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}
