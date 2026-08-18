package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/workflow"
)

var (
	// ErrRecoveryFailed 表示逐级恢复触发了健康门禁或平台执行失败。
	ErrRecoveryFailed = errors.New("traffic recovery failed")
	// ErrRecoveryTimedOut 表示恢复 Operation 在允许时间内没有得到确定结论。
	ErrRecoveryTimedOut = errors.New("traffic recovery timed out")
)

// RecoveryProcessor 根据冻结 Plan 发起逐级恢复，并把确定结果写回 Workflow。
// Agent 不能指定恢复步长，也不能直接修改路由权重。
type RecoveryProcessor struct {
	platform         platform.ToolOpsPlatform
	workflow         *workflow.IncidentWorkflow
	submitTimeout    time.Duration
	pollInitial      time.Duration
	pollMaximum      time.Duration
	operationTimeout time.Duration
}

func newRecoveryProcessor(processor *PlanProcessor) (*RecoveryProcessor, error) {
	if processor == nil || processor.platform == nil || processor.workflow == nil {
		return nil, fmt.Errorf("initialized plan processor is required")
	}
	if processor.submitTimeout <= 0 || processor.pollInitial <= 0 ||
		processor.pollMaximum < processor.pollInitial || processor.operationTimeout <= 0 {
		return nil, fmt.Errorf("recovery processor durations are invalid")
	}
	return &RecoveryProcessor{
		platform: processor.platform, workflow: processor.workflow,
		submitTimeout: processor.submitTimeout, pollInitial: processor.pollInitial,
		pollMaximum: processor.pollMaximum, operationTimeout: processor.operationTimeout,
	}, nil
}

// Run 使用稳定幂等键发起或恢复当前 Plan 的逐级恢复，并轮询到终态。
func (p *RecoveryProcessor) Run(ctx context.Context) (platform.Operation, error) {
	if p == nil || p.platform == nil || p.workflow == nil {
		return platform.Operation{}, fmt.Errorf("recovery processor is not initialized")
	}
	snapshot := p.workflow.Snapshot()
	if snapshot.State != workflow.StateRecovering {
		return platform.Operation{}, fmt.Errorf("%w: traffic recovery is not allowed in state %q", workflow.ErrInvalidTransition, snapshot.State)
	}
	if snapshot.Plan == nil {
		return platform.Operation{}, fmt.Errorf("recovering workflow has no frozen plan")
	}
	request := platform.RecoveryRequest{
		IncidentID: snapshot.IncidentID, RouteID: snapshot.Plan.ProbeRouteID,
		PolicyID: snapshot.Plan.RecoveryPolicyID, IdempotencyKey: snapshot.Plan.ID + ":recovery",
	}
	callCtx, cancel := context.WithTimeout(ctx, p.submitTimeout)
	operation, callErr := p.platform.RequestRecovery(callCtx, request)
	cancel()
	if validateErr := validateRecoveryOperation(operation, request); validateErr != nil {
		if callErr != nil && strings.TrimSpace(operation.ID) == "" {
			failure := classifyControllerError(workflow.FailureStageRecovery, "recovery_submit_failed",
				"恢复请求未返回可对账的 Operation", snapshot.Plan.ID, "", operation, callErr)
			return operation, errors.Join(callErr, validateErr, p.recordFailure(failure))
		}
		failure := invalidResponseFailure(workflow.FailureStageRecovery, "invalid_recovery_operation",
			"恢复平台返回了与冻结请求不匹配的 Operation", snapshot.Plan.ID, "", operation)
		failure.Message = errors.Join(callErr, validateErr).Error()
		recordErr := p.recordFailure(failure)
		if callErr != nil {
			return operation, errors.Join(callErr, validateErr, recordErr)
		}
		return operation, errors.Join(validateErr, recordErr)
	}
	if callErr != nil && !isTerminalOperation(operation.Status) {
		failure := classifyControllerError(workflow.FailureStageRecovery, "recovery_submit_failed",
			"恢复请求暂时未获得确定结果", snapshot.Plan.ID, "", operation, callErr)
		return operation, errors.Join(callErr, p.recordFailure(failure))
	}

	deadline := time.Now().Add(p.operationTimeout)
	pollInterval := p.pollInitial
	for {
		terminal, err := p.reconcile(snapshot, request, operation)
		if terminal {
			return operation, errors.Join(callErr, err)
		}
		if err != nil {
			return operation, err
		}
		if !time.Now().Before(deadline) {
			reason := "recovery operation exceeded its execution deadline"
			failure := workflow.StageFailure{
				Stage: workflow.FailureStageRecovery, Category: workflow.FailureCategoryTimedOut,
				Code: "recovery_operation_timed_out", SafeSummary: "逐级恢复 Operation 超过执行时限",
				Message: reason, NextAction: workflow.FailureNextReinvestigate,
				PlanID: snapshot.Plan.ID, OperationID: operation.ID, OperationStatus: string(operation.Status),
			}
			return operation, p.fail(snapshot, request, operation, reason, failure, ErrRecoveryTimedOut)
		}
		wait := pollInterval
		if remaining := time.Until(deadline); wait > remaining {
			wait = remaining
		}
		if err := waitContext(ctx, wait); err != nil {
			failure := unknownResultFailure(workflow.FailureStageRecovery, "recovery_interrupted",
				"恢复等待被中断，需要先确认原 Operation 状态", snapshot.Plan.ID, "", operation, err)
			return operation, errors.Join(err, p.recordFailure(failure))
		}
		queryCtx, cancelQuery := context.WithTimeout(ctx, p.submitTimeout)
		operation, err = p.platform.GetOperation(queryCtx, platform.OperationQuery{
			IncidentID: snapshot.IncidentID, OperationID: operation.ID,
		})
		cancelQuery()
		if err != nil {
			wrapped := fmt.Errorf("query recovery operation: %w", err)
			failure := classifyControllerError(workflow.FailureStageRecovery, "recovery_query_failed",
				"暂时无法查询恢复 Operation", snapshot.Plan.ID, "", operation, wrapped)
			return operation, errors.Join(wrapped, p.recordFailure(failure))
		}
		if err := validateRecoveryOperation(operation, request); err != nil {
			failure := invalidResponseFailure(workflow.FailureStageRecovery, "invalid_recovery_operation",
				"恢复平台返回了与冻结请求不匹配的 Operation", snapshot.Plan.ID, "", operation)
			failure.Message = err.Error()
			return operation, errors.Join(err, p.recordFailure(failure))
		}
		if pollInterval < p.pollMaximum {
			pollInterval *= 2
			if pollInterval > p.pollMaximum {
				pollInterval = p.pollMaximum
			}
		}
	}
}

func (p *RecoveryProcessor) reconcile(snapshot workflow.Snapshot, request platform.RecoveryRequest, operation platform.Operation) (bool, error) {
	switch operation.Status {
	case platform.OperationPending, platform.OperationRunning:
		return false, nil
	case platform.OperationSucceeded:
		outcome, _ := operation.Result["outcome"].(string)
		switch strings.TrimSpace(outcome) {
		case "healthy":
			_, err := p.workflow.Apply(workflow.Event{
				Type: workflow.EventStageSucceeded, Actor: workflow.ActorController,
				Reason:   "all gradual recovery steps satisfied the frozen policy",
				Metadata: recoveryMetadata(snapshot.Plan.ID, request, operation, "healthy"),
			})
			return true, err
		case "hard_stop":
			reason := recoveryFailureReason(operation, "recovery reached a hard-stop condition")
			failure := workflow.StageFailure{
				Stage: workflow.FailureStageRecovery, Category: workflow.FailureCategoryHealthGateFailed,
				Code: "recovery_health_gate_failed", SafeSummary: "逐级恢复触发健康门禁，已停止继续放量",
				Message: reason, NextAction: workflow.FailureNextReinvestigate,
				PlanID: snapshot.Plan.ID, OperationID: operation.ID, OperationStatus: string(operation.Status),
			}
			return true, p.fail(snapshot, request, operation, reason, failure, ErrRecoveryFailed)
		default:
			err := fmt.Errorf("succeeded recovery operation has invalid outcome %q", outcome)
			failure := invalidResponseFailure(workflow.FailureStageRecovery, "invalid_recovery_outcome",
				"恢复平台返回了无法识别的成功结果", snapshot.Plan.ID, "", operation)
			failure.Message = err.Error()
			return false, errors.Join(err, p.recordFailure(failure))
		}
	case platform.OperationFailed:
		reason := recoveryFailureReason(operation, "traffic recovery operation did not succeed")
		failure := workflow.StageFailure{
			Stage: workflow.FailureStageRecovery, Category: workflow.FailureCategoryExecutionFailed,
			Code: "recovery_operation_failed", SafeSummary: "逐级恢复 Operation 执行失败",
			Message: reason, NextAction: workflow.FailureNextReinvestigate,
			PlanID: snapshot.Plan.ID, OperationID: operation.ID, OperationStatus: string(operation.Status),
		}
		return true, p.fail(snapshot, request, operation, reason, failure, ErrRecoveryFailed)
	case platform.OperationRejected:
		reason := recoveryFailureReason(operation, "traffic recovery operation was rejected")
		failure := workflow.StageFailure{
			Stage: workflow.FailureStageRecovery, Category: workflow.FailureCategoryPreconditionChanged,
			Code: "recovery_operation_rejected", SafeSummary: "恢复请求被平台拒绝，需要重新调查当前状态",
			Message: reason, NextAction: workflow.FailureNextReinvestigate,
			PlanID: snapshot.Plan.ID, OperationID: operation.ID, OperationStatus: string(operation.Status),
		}
		return true, p.fail(snapshot, request, operation, reason, failure, ErrRecoveryFailed)
	case platform.OperationCancelled:
		reason := recoveryFailureReason(operation, "traffic recovery operation was cancelled")
		failure := unknownResultFailure(workflow.FailureStageRecovery, "recovery_operation_cancelled",
			"恢复 Operation 被取消，需要重新调查当前流量状态", snapshot.Plan.ID, "", operation, errors.New(reason))
		failure.NextAction = workflow.FailureNextReinvestigate
		return true, p.fail(snapshot, request, operation, reason, failure, ErrRecoveryFailed)
	default:
		err := fmt.Errorf("recovery operation has unknown status %q", operation.Status)
		failure := invalidResponseFailure(workflow.FailureStageRecovery, "unknown_recovery_status",
			"恢复平台返回了无法识别的 Operation 状态", snapshot.Plan.ID, "", operation)
		failure.Message = err.Error()
		return false, errors.Join(err, p.recordFailure(failure))
	}
}

func (p *RecoveryProcessor) fail(snapshot workflow.Snapshot, request platform.RecoveryRequest,
	operation platform.Operation, reason string, failure workflow.StageFailure, cause error,
) error {
	outcome, _ := operation.Result["outcome"].(string)
	_, transitionErr := p.workflow.Apply(workflow.Event{
		Type: workflow.EventStageFailed, Actor: workflow.ActorController, Reason: strings.TrimSpace(reason),
		Metadata: recoveryMetadata(snapshot.Plan.ID, request, operation, strings.TrimSpace(outcome)), Failure: &failure,
	})
	return errors.Join(cause, transitionErr)
}

func (p *RecoveryProcessor) recordFailure(failure workflow.StageFailure) error {
	_, err := p.workflow.RecordFailure(failure)
	return err
}

func validateRecoveryOperation(operation platform.Operation, request platform.RecoveryRequest) error {
	if strings.TrimSpace(operation.ID) == "" {
		return fmt.Errorf("recovery operation ID is required")
	}
	if operation.IncidentID != request.IncidentID || operation.Kind != platform.OperationRecovery ||
		operation.IdempotencyKey != request.IdempotencyKey {
		return fmt.Errorf("platform operation does not match the frozen recovery request")
	}
	if routeID, _ := operation.Result["route_id"].(string); routeID != request.RouteID {
		return fmt.Errorf("platform recovery operation route does not match the frozen plan")
	}
	return nil
}

func recoveryFailureReason(operation platform.Operation, fallback string) string {
	if reason, _ := operation.Result["reason"].(string); strings.TrimSpace(reason) != "" {
		return strings.TrimSpace(reason)
	}
	if strings.TrimSpace(operation.Message) != "" {
		return strings.TrimSpace(operation.Message)
	}
	return fallback
}

func recoveryMetadata(planID string, request platform.RecoveryRequest, operation platform.Operation, outcome string) map[string]string {
	return map[string]string{
		"plan_id": planID, "operation_id": operation.ID, "route_id": request.RouteID,
		"policy_id": request.PolicyID, "outcome": outcome,
	}
}
