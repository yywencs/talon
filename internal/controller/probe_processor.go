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
		if callErr != nil {
			return operation, errors.Join(callErr, validateErr)
		}
		return operation, validateErr
	}
	if callErr != nil && !isTerminalOperation(operation.Status) {
		return operation, callErr
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
			return operation, p.fail(snapshot, operation, "probe operation exceeded its execution deadline", ErrProbeTimedOut)
		}

		wait := pollInterval
		if remaining := time.Until(deadline); wait > remaining {
			wait = remaining
		}
		if err := waitContext(ctx, wait); err != nil {
			return operation, err
		}
		queryCtx, cancelQuery := context.WithTimeout(ctx, p.submitTimeout)
		operation, err = p.platform.GetOperation(queryCtx, platform.OperationQuery{
			IncidentID: snapshot.IncidentID, OperationID: operation.ID,
		})
		cancelQuery()
		if err != nil {
			return operation, fmt.Errorf("query probe operation: %w", err)
		}
		if err := validateProbeOperation(operation, request); err != nil {
			return operation, err
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
			return true, p.fail(snapshot, operation, reason, ErrProbeFailed)
		default:
			return false, fmt.Errorf("succeeded probe operation has invalid outcome %q", outcome)
		}
	case platform.OperationFailed, platform.OperationRejected, platform.OperationCancelled:
		reason := probeFailureReason(operation, "traffic probe operation did not succeed")
		return true, p.fail(snapshot, operation, reason, ErrProbeFailed)
	default:
		return false, fmt.Errorf("probe operation has unknown status %q", operation.Status)
	}
}

func (p *ProbeProcessor) fail(snapshot workflow.Snapshot, operation platform.Operation, reason string, cause error) error {
	request := platform.ProbeRequest{
		IncidentID: snapshot.IncidentID, RouteID: snapshot.Plan.ProbeRouteID,
		PolicyID: snapshot.Plan.RecoveryPolicyID, IdempotencyKey: snapshot.Plan.ID + ":probe",
	}
	outcome, _ := operation.Result["outcome"].(string)
	_, transitionErr := p.workflow.Apply(workflow.Event{
		Type: workflow.EventStageFailed, Actor: workflow.ActorController, Reason: strings.TrimSpace(reason),
		Metadata: probeMetadata(snapshot.Plan.ID, request, operation, strings.TrimSpace(outcome)),
	})
	return errors.Join(cause, transitionErr)
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
