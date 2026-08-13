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
			return operation, p.fail(snapshot, request, operation,
				"recovery operation exceeded its execution deadline", ErrRecoveryTimedOut)
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
			return operation, fmt.Errorf("query recovery operation: %w", err)
		}
		if err := validateRecoveryOperation(operation, request); err != nil {
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
			return true, p.fail(snapshot, request, operation, reason, ErrRecoveryFailed)
		default:
			return false, fmt.Errorf("succeeded recovery operation has invalid outcome %q", outcome)
		}
	case platform.OperationFailed, platform.OperationRejected, platform.OperationCancelled:
		reason := recoveryFailureReason(operation, "traffic recovery operation did not succeed")
		return true, p.fail(snapshot, request, operation, reason, ErrRecoveryFailed)
	default:
		return false, fmt.Errorf("recovery operation has unknown status %q", operation.Status)
	}
}

func (p *RecoveryProcessor) fail(snapshot workflow.Snapshot, request platform.RecoveryRequest,
	operation platform.Operation, reason string, cause error,
) error {
	outcome, _ := operation.Result["outcome"].(string)
	_, transitionErr := p.workflow.Apply(workflow.Event{
		Type: workflow.EventStageFailed, Actor: workflow.ActorController, Reason: strings.TrimSpace(reason),
		Metadata: recoveryMetadata(snapshot.Plan.ID, request, operation, strings.TrimSpace(outcome)),
	})
	return errors.Join(cause, transitionErr)
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
