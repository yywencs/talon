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
	// ErrProbeFailed 表示平台探测已得到确定的失败结论，Workflow 已回到重新调查阶段。
	ErrProbeFailed = errors.New("traffic probe failed")
	// ErrProbeTimedOut 表示探测 Operation 在 Controller 的时间上限内没有结束。
	ErrProbeTimedOut = errors.New("traffic probe timed out")
)

// ProbeProcessor 负责执行冻结 Plan 中的流量探测，并把平台 Operation 的确定结果
// 转换为 Workflow 事件。它不让 Agent 决定探测是否通过。
type ProbeProcessor struct {
	platform         platform.ToolOpsPlatform
	workflow         *workflow.IncidentWorkflow
	submitTimeout    time.Duration
	pollInitial      time.Duration
	pollMaximum      time.Duration
	operationTimeout time.Duration
}

// newProbeProcessor 复用 PlanProcessor 的平台连接与异步调用时间边界。
func newProbeProcessor(processor *PlanProcessor) (*ProbeProcessor, error) {
	if processor == nil || processor.platform == nil || processor.workflow == nil {
		return nil, fmt.Errorf("initialized plan processor is required")
	}
	if processor.submitTimeout <= 0 || processor.pollInitial <= 0 ||
		processor.pollMaximum < processor.pollInitial || processor.operationTimeout <= 0 {
		return nil, fmt.Errorf("probe processor durations are invalid")
	}
	return &ProbeProcessor{
		platform: processor.platform, workflow: processor.workflow,
		submitTimeout: processor.submitTimeout, pollInitial: processor.pollInitial,
		pollMaximum: processor.pollMaximum, operationTimeout: processor.operationTimeout,
	}, nil
}

// Run 使用稳定幂等键发起或恢复当前 Plan 的探测，并轮询到确定终态。
// Controller 重试 Run 时会再次提交同一请求；Platform 必须返回原 Operation，
// 因而不会重复创建探测流量。
func (p *ProbeProcessor) Run(ctx context.Context) (platform.Operation, error) {
	if p == nil || p.platform == nil || p.workflow == nil {
		return platform.Operation{}, fmt.Errorf("probe processor is not initialized")
	}
	snapshot := p.workflow.Snapshot()
	if snapshot.State != workflow.StateProbing {
		return platform.Operation{}, fmt.Errorf("%w: traffic probe is not allowed in state %q", workflow.ErrInvalidTransition, snapshot.State)
	}
	if snapshot.Plan == nil {
		return platform.Operation{}, fmt.Errorf("probing workflow has no frozen plan")
	}

	request := platform.ProbeRequest{
		IncidentID: snapshot.IncidentID, RouteID: snapshot.Plan.ProbeRouteID,
		PolicyID: snapshot.Plan.RecoveryPolicyID, IdempotencyKey: snapshot.Plan.ID + ":probe",
	}
	callCtx, cancel := context.WithTimeout(ctx, p.submitTimeout)
	operation, callErr := p.platform.RequestProbe(callCtx, request)
	cancel()
	if validateErr := validateProbeOperation(operation, request); validateErr != nil {
		if callErr != nil && strings.TrimSpace(operation.ID) == "" {
			failure := classifyControllerError(workflow.FailureStageProbe, "probe_submit_failed",
				"探测请求未返回可对账的 Operation", snapshot.Plan.ID, "", operation, callErr)
			return operation, errors.Join(callErr, validateErr, p.recordFailure(failure))
		}
		failure := invalidResponseFailure(workflow.FailureStageProbe, "invalid_probe_operation",
			"探测平台返回了与冻结请求不匹配的 Operation", snapshot.Plan.ID, "", operation)
		failure.Message = errors.Join(callErr, validateErr).Error()
		recordErr := p.recordFailure(failure)
		if callErr != nil {
			return operation, errors.Join(callErr, validateErr, recordErr)
		}
		return operation, errors.Join(validateErr, recordErr)
	}
	if callErr != nil && !isTerminalOperation(operation.Status) {
		failure := classifyControllerError(workflow.FailureStageProbe, "probe_submit_failed",
			"探测请求暂时未获得确定结果", snapshot.Plan.ID, "", operation, callErr)
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
			reason := "probe operation exceeded its execution deadline"
			failure := workflow.StageFailure{
				Stage: workflow.FailureStageProbe, Category: workflow.FailureCategoryTimedOut,
				Code: "probe_operation_timed_out", SafeSummary: "探测 Operation 超过执行时限",
				Message: reason, NextAction: workflow.FailureNextReinvestigate,
				PlanID: snapshot.Plan.ID, OperationID: operation.ID, OperationStatus: string(operation.Status),
			}
			return operation, p.fail(snapshot, operation, reason, failure, ErrProbeTimedOut)
		}

		wait := pollInterval
		if remaining := time.Until(deadline); wait > remaining {
			wait = remaining
		}
		if err := waitContext(ctx, wait); err != nil {
			failure := unknownResultFailure(workflow.FailureStageProbe, "probe_interrupted",
				"探测等待被中断，需要先确认原 Operation 状态", snapshot.Plan.ID, "", operation, err)
			return operation, errors.Join(err, p.recordFailure(failure))
		}
		queryCtx, cancelQuery := context.WithTimeout(ctx, p.submitTimeout)
		operation, err = p.platform.GetOperation(queryCtx, platform.OperationQuery{
			IncidentID: snapshot.IncidentID, OperationID: operation.ID,
		})
		cancelQuery()
		if err != nil {
			wrapped := fmt.Errorf("query probe operation: %w", err)
			failure := classifyControllerError(workflow.FailureStageProbe, "probe_query_failed",
				"暂时无法查询探测 Operation", snapshot.Plan.ID, "", operation, wrapped)
			return operation, errors.Join(wrapped, p.recordFailure(failure))
		}
		if err := validateProbeOperation(operation, request); err != nil {
			failure := invalidResponseFailure(workflow.FailureStageProbe, "invalid_probe_operation",
				"探测平台返回了与冻结请求不匹配的 Operation", snapshot.Plan.ID, "", operation)
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

// reconcile 将探测 Operation 的平台状态和业务 outcome 收敛为 Workflow 状态。
func (p *ProbeProcessor) reconcile(snapshot workflow.Snapshot, request platform.ProbeRequest, operation platform.Operation) (bool, error) {
	switch operation.Status {
	case platform.OperationPending, platform.OperationRunning:
		return false, nil
	case platform.OperationSucceeded:
		outcome, _ := operation.Result["outcome"].(string)
		switch strings.TrimSpace(outcome) {
		case "healthy":
			_, err := p.workflow.Apply(workflow.Event{
				Type: workflow.EventStageSucceeded, Actor: workflow.ActorController,
				Reason:   "traffic probe satisfied the frozen recovery policy",
				Metadata: probeMetadata(snapshot.Plan.ID, request, operation, "healthy"),
			})
			return true, err
		case "hard_stop":
			reason := probeFailureReason(operation, "traffic probe reached a hard-stop condition")
			failure := workflow.StageFailure{
				Stage: workflow.FailureStageProbe, Category: workflow.FailureCategoryHealthGateFailed,
				Code: "probe_health_gate_failed", SafeSummary: "探测触发健康门禁，不能进入流量恢复",
				Message: reason, NextAction: workflow.FailureNextReinvestigate,
				PlanID: snapshot.Plan.ID, OperationID: operation.ID, OperationStatus: string(operation.Status),
			}
			return true, p.fail(snapshot, operation, reason, failure, ErrProbeFailed)
		default:
			err := fmt.Errorf("succeeded probe operation has invalid outcome %q", outcome)
			failure := invalidResponseFailure(workflow.FailureStageProbe, "invalid_probe_outcome",
				"探测平台返回了无法识别的成功结果", snapshot.Plan.ID, "", operation)
			failure.Message = err.Error()
			return false, errors.Join(err, p.recordFailure(failure))
		}
	case platform.OperationFailed:
		reason := probeFailureReason(operation, "traffic probe operation did not succeed")
		failure := workflow.StageFailure{
			Stage: workflow.FailureStageProbe, Category: workflow.FailureCategoryExecutionFailed,
			Code: "probe_operation_failed", SafeSummary: "探测 Operation 执行失败",
			Message: reason, NextAction: workflow.FailureNextReinvestigate,
			PlanID: snapshot.Plan.ID, OperationID: operation.ID, OperationStatus: string(operation.Status),
		}
		return true, p.fail(snapshot, operation, reason, failure, ErrProbeFailed)
	case platform.OperationRejected:
		reason := probeFailureReason(operation, "traffic probe operation was rejected")
		failure := workflow.StageFailure{
			Stage: workflow.FailureStageProbe, Category: workflow.FailureCategoryPreconditionChanged,
			Code: "probe_operation_rejected", SafeSummary: "探测请求被平台拒绝，需要重新调查当前状态",
			Message: reason, NextAction: workflow.FailureNextReinvestigate,
			PlanID: snapshot.Plan.ID, OperationID: operation.ID, OperationStatus: string(operation.Status),
		}
		return true, p.fail(snapshot, operation, reason, failure, ErrProbeFailed)
	case platform.OperationCancelled:
		reason := probeFailureReason(operation, "traffic probe operation was cancelled")
		failure := unknownResultFailure(workflow.FailureStageProbe, "probe_operation_cancelled",
			"探测 Operation 被取消，需要重新调查后再决定是否探测", snapshot.Plan.ID, "", operation, errors.New(reason))
		failure.NextAction = workflow.FailureNextReinvestigate
		return true, p.fail(snapshot, operation, reason, failure, ErrProbeFailed)
	default:
		err := fmt.Errorf("probe operation has unknown status %q", operation.Status)
		failure := invalidResponseFailure(workflow.FailureStageProbe, "unknown_probe_status",
			"探测平台返回了无法识别的 Operation 状态", snapshot.Plan.ID, "", operation)
		failure.Message = err.Error()
		return false, errors.Join(err, p.recordFailure(failure))
	}
}

func (p *ProbeProcessor) fail(snapshot workflow.Snapshot, operation platform.Operation, reason string, failure workflow.StageFailure, cause error) error {
	request := platform.ProbeRequest{
		IncidentID: snapshot.IncidentID, RouteID: snapshot.Plan.ProbeRouteID,
		PolicyID: snapshot.Plan.RecoveryPolicyID, IdempotencyKey: snapshot.Plan.ID + ":probe",
	}
	outcome, _ := operation.Result["outcome"].(string)
	_, transitionErr := p.workflow.Apply(workflow.Event{
		Type: workflow.EventStageFailed, Actor: workflow.ActorController, Reason: strings.TrimSpace(reason),
		Metadata: probeMetadata(snapshot.Plan.ID, request, operation, strings.TrimSpace(outcome)), Failure: &failure,
	})
	return errors.Join(cause, transitionErr)
}

func (p *ProbeProcessor) recordFailure(failure workflow.StageFailure) error {
	_, err := p.workflow.RecordFailure(failure)
	return err
}

func validateProbeOperation(operation platform.Operation, request platform.ProbeRequest) error {
	if strings.TrimSpace(operation.ID) == "" {
		return fmt.Errorf("probe operation ID is required")
	}
	if operation.IncidentID != request.IncidentID || operation.Kind != platform.OperationProbe ||
		operation.IdempotencyKey != request.IdempotencyKey {
		return fmt.Errorf("platform operation does not match the frozen probe request")
	}
	return nil
}

func probeFailureReason(operation platform.Operation, fallback string) string {
	if reason, _ := operation.Result["reason"].(string); strings.TrimSpace(reason) != "" {
		return strings.TrimSpace(reason)
	}
	if strings.TrimSpace(operation.Message) != "" {
		return strings.TrimSpace(operation.Message)
	}
	return fallback
}

func probeMetadata(planID string, request platform.ProbeRequest, operation platform.Operation, outcome string) map[string]string {
	return map[string]string{
		"plan_id": planID, "operation_id": operation.ID, "route_id": request.RouteID,
		"policy_id": request.PolicyID, "outcome": outcome,
	}
}
