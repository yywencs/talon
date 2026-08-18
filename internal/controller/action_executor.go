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

// ErrActionExecutionUnknown 表示平台调用可能已经产生副作用，但 Controller 无法确认最终结果。
var ErrActionExecutionUnknown = errors.New("action execution result is unknown")

// ErrOperationTimedOut 表示异步 Operation 超过允许的最长执行时间。
var ErrOperationTimedOut = errors.New("platform operation timed out")

// ExecuteNext 领取并处理当前 Plan 的下一个 Action。
// 同一 Plan 严格串行；超时接管始终复用稳定幂等键，因此请求可重试而业务副作用最多发生一次。
func (p *PlanProcessor) ExecuteNext(ctx context.Context) (execution.Record, error) {
	if p == nil || p.platform == nil || p.workflow == nil || p.executionStore == nil {
		return execution.Record{}, fmt.Errorf("plan processor execution is not initialized")
	}
	if err := ctx.Err(); err != nil {
		return execution.Record{}, err
	}
	snapshot := p.workflow.Snapshot()
	// 只有 Policy 已放行、Workflow 已进入修复阶段的冻结 Plan 才允许执行。
	if snapshot.State != workflow.StateRemediating {
		return execution.Record{}, fmt.Errorf("%w: action execution is not allowed in state %q", workflow.ErrInvalidTransition, snapshot.State)
	}
	if snapshot.Plan == nil {
		return execution.Record{}, fmt.Errorf("remediating workflow has no frozen plan")
	}
	if err := p.validateExecutablePlan(ctx, snapshot); err != nil {
		return execution.Record{}, err
	}
	// 一次性登记完整 Action 清单，使后续领取、串行控制和故障恢复都以数据库记录为准。
	records, err := p.executionStore.Prepare(ctx, executionSpecs(snapshot))
	if err != nil {
		return execution.Record{}, fmt.Errorf("prepare action executions: %w", err)
	}
	if terminal, terminalErr := p.reconcileExecutionStage(snapshot, records); terminal {
		return lastExecution(records), terminalErr
	}

	// ClaimNext 只会领取序号最小且前置 Action 均已成功的记录，并为当前 Worker 建立租约。
	claimed, err := p.executionStore.ClaimNext(ctx, execution.Claim{
		PlanID: snapshot.Plan.ID, OwnerID: p.workerID, LeaseDuration: p.leaseDuration,
	})
	if err != nil {
		if errors.Is(err, execution.ErrNoClaimable) {
			return execution.Record{}, err
		}
		return execution.Record{}, fmt.Errorf("claim next action execution: %w", err)
	}
	action := plannedAction(snapshot.Plan.Actions, claimed.ActionID)
	// 执行前再次核对持久化记录，避免同一 Action ID 被绑定到不同的冻结内容。
	if action == nil || action.Digest != claimed.ActionDigest || action.ToolName != claimed.ToolName {
		return claimed, fmt.Errorf("%w: claimed execution does not match frozen action", execution.ErrConflict)
	}
	if claimed.OperationDeadline != nil && !time.Now().UTC().Before(*claimed.OperationDeadline) {
		return p.finishTimedOutAction(ctx, snapshot, claimed)
	}

	var operation platform.Operation
	var callErr error
	// 已记录 OperationID 时优先查询原操作，避免恢复或重试时重复提交平台请求。
	if claimed.OperationID != "" {
		queryCtx, cancelQuery := context.WithTimeout(ctx, p.submitTimeout)
		operation, callErr = p.platform.GetOperation(queryCtx, platform.OperationQuery{
			IncidentID: snapshot.IncidentID, OperationID: claimed.OperationID,
		})
		cancelQuery()
		// Operation 索引暂时不可见时，使用原幂等键重新提交比生成新请求更安全。
		// 符合 Platform 契约的实现会返回原 Operation，而不会再次产生副作用。
		if errors.Is(callErr, platform.ErrNotFound) {
			operation, callErr = p.executeRemediationWithLease(ctx, snapshot.IncidentID, *action, claimed)
		}
	} else {
		operation, callErr = p.executeRemediationWithLease(ctx, snapshot.IncidentID, *action, claimed)
	}
	// 即使上游请求已取消，也尽力把平台调用结果写回，避免副作用已发生而本地仍显示 running。
	persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelPersist()
	if callErr != nil {
		// 调用返回错误但同时携带可信终态时，仍按终态完成记录；否则标记 unknown 等待对账。
		if isTerminalOperation(operation.Status) && operation.ID != "" {
			if validateErr := validatePlatformOperation(operation, snapshot.IncidentID, *action, claimed.IdempotencyKey); validateErr != nil {
				unknown, storeErr := p.executionStore.MarkUnknown(persistCtx, claimed.ActionID, p.workerID,
					operation.ID, string(operation.Status), validateErr.Error(), p.pollSchedule(claimed))
				failure := unknownResultFailure(workflow.FailureStageRemediation, "remediation_operation_mismatch",
					"修复平台返回的 Operation 与冻结 Action 不匹配，需要先对账", snapshot.Plan.ID, claimed.ActionID,
					operation, errors.Join(callErr, validateErr))
				recordErr := p.recordFailure(failure)
				if storeErr != nil {
					return claimed, errors.Join(callErr, validateErr, storeErr, recordErr)
				}
				return unknown, errors.Join(ErrActionExecutionUnknown, callErr, validateErr, recordErr)
			}
			return p.finishAction(persistCtx, snapshot, claimed, operation, callErr)
		}
		unknown, storeErr := p.executionStore.MarkUnknown(persistCtx, claimed.ActionID, p.workerID,
			operation.ID, string(operation.Status), callErr.Error(), p.pollSchedule(claimed))
		failure := unknownResultFailure(workflow.FailureStageRemediation, "remediation_result_unknown",
			"修复调用结果未知，需要查询原 Operation 后再决定后续动作", snapshot.Plan.ID, claimed.ActionID,
			operation, callErr)
		recordErr := p.recordFailure(failure)
		if storeErr != nil {
			return claimed, errors.Join(callErr, fmt.Errorf("persist unknown action result: %w", storeErr), recordErr)
		}
		return unknown, errors.Join(ErrActionExecutionUnknown, callErr, recordErr)
	}
	if err := validatePlatformOperation(operation, snapshot.IncidentID, *action, claimed.IdempotencyKey); err != nil {
		unknown, storeErr := p.executionStore.MarkUnknown(persistCtx, claimed.ActionID, p.workerID,
			operation.ID, string(operation.Status), err.Error(), p.pollSchedule(claimed))
		failure := unknownResultFailure(workflow.FailureStageRemediation, "remediation_operation_mismatch",
			"修复平台返回的 Operation 与冻结 Action 不匹配，需要先对账", snapshot.Plan.ID, claimed.ActionID,
			operation, err)
		recordErr := p.recordFailure(failure)
		if storeErr != nil {
			return claimed, errors.Join(err, storeErr, recordErr)
		}
		return unknown, errors.Join(ErrActionExecutionUnknown, err, recordErr)
	}

	// 平台非终态只记录 Operation，终态则完成当前 Action 并汇总整个执行阶段。
	switch operation.Status {
	case platform.OperationPending, platform.OperationRunning:
		recorded, err := p.executionStore.RecordOperation(persistCtx, claimed.ActionID, p.workerID,
			operation.ID, string(operation.Status), p.pollSchedule(claimed))
		if err != nil {
			return claimed, fmt.Errorf("persist running platform operation: %w", err)
		}
		return recorded, nil
	case platform.OperationSucceeded, platform.OperationFailed, platform.OperationRejected, platform.OperationCancelled:
		return p.finishAction(persistCtx, snapshot, claimed, operation, nil)
	default:
		unknown, err := p.executionStore.MarkUnknown(persistCtx, claimed.ActionID, p.workerID,
			operation.ID, string(operation.Status), "platform returned unknown operation status", p.pollSchedule(claimed))
		failure := unknownResultFailure(workflow.FailureStageRemediation, "unknown_remediation_status",
			"修复平台返回了无法识别的 Operation 状态，需要先对账", snapshot.Plan.ID, claimed.ActionID,
			operation, errors.New("platform returned unknown operation status"))
		recordErr := p.recordFailure(failure)
		if err != nil {
			return claimed, errors.Join(err, recordErr)
		}
		return unknown, errors.Join(ErrActionExecutionUnknown, recordErr)
	}
}

// RenewActionLease 允许长时间运行的 Worker 主动续租；正常提交调用期间 Controller 也会自动续租。
func (p *PlanProcessor) RenewActionLease(ctx context.Context, actionID string) (execution.Record, error) {
	if p == nil || p.executionStore == nil || p.workerID == "" || p.leaseDuration <= 0 {
		return execution.Record{}, fmt.Errorf("plan processor execution is not initialized")
	}
	return p.executionStore.Renew(ctx, actionID, p.workerID, p.leaseDuration)
}

// executeRemediationWithLease 在短提交超时内获取异步 Operation，并在提交期间续租当前 Action。
func (p *PlanProcessor) executeRemediationWithLease(ctx context.Context, incidentID string, action workflow.PlannedAction, claimed execution.Record) (platform.Operation, error) {
	arguments := cloneMap(action.Arguments)
	expectedVersion, _ := arguments["expected_version"].(string)
	// Controller 统一控制这些协议字段，禁止冻结参数覆盖真实执行语义和幂等键。
	delete(arguments, "idempotency_key")
	delete(arguments, "expected_version")
	delete(arguments, "dry_run")
	request := platform.RemediationRequest{
		IncidentID: incidentID, ToolName: action.ToolName, Arguments: arguments,
		ExpectedVersion: strings.TrimSpace(expectedVersion), DryRun: false,
		IdempotencyKey: claimed.IdempotencyKey,
	}
	callCtx, cancel := context.WithTimeout(ctx, p.submitTimeout)
	defer cancel()
	renewal := make(chan error, 1)
	done := make(chan struct{})
	interval := p.leaseDuration / 3
	if interval <= 0 {
		interval = time.Millisecond
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-callCtx.Done():
				return
			case <-ticker.C:
				if _, err := p.executionStore.Renew(callCtx, claimed.ActionID, p.workerID, p.leaseDuration); err != nil {
					// 失去租约后立即取消平台调用，避免两个 Worker 同时推进同一 Action。
					renewal <- err
					cancel()
					return
				}
			}
		}
	}()
	operation, err := p.platform.ExecuteRemediation(callCtx, request)
	close(done)
	select {
	case renewErr := <-renewal:
		if err == nil {
			err = fmt.Errorf("renew action execution lease: %w", renewErr)
		} else {
			err = errors.Join(err, renewErr)
		}
	default:
	}
	return operation, err
}

// pollSchedule 根据领取次数做指数退避，并保留首次提交时确定的 Operation 总截止时间。
func (p *PlanProcessor) pollSchedule(claimed execution.Record) execution.PollSchedule {
	now := time.Now().UTC()
	deadline := now.Add(p.operationTimeout)
	if claimed.OperationDeadline != nil {
		deadline = claimed.OperationDeadline.UTC()
	}
	interval := p.pollInitial
	for attempt := 1; attempt < claimed.Attempt && interval < p.pollMaximum; attempt++ {
		interval *= 2
		if interval > p.pollMaximum {
			interval = p.pollMaximum
		}
	}
	next := now.Add(interval)
	if next.After(deadline) {
		next = deadline
	}
	return execution.PollSchedule{NextPollAt: next, OperationDeadline: deadline}
}

// finishTimedOutAction 把长期 pending/running/unknown 的 Operation 收敛为失败并重新进入调查。
func (p *PlanProcessor) finishTimedOutAction(ctx context.Context, snapshot workflow.Snapshot, claimed execution.Record) (execution.Record, error) {
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	message := "platform operation exceeded its execution deadline"
	recorded, err := p.executionStore.Complete(persistCtx, claimed.ActionID, p.workerID,
		claimed.OperationID, "timed_out", execution.StatusFailed, message)
	if err != nil {
		return claimed, fmt.Errorf("complete timed out action execution: %w", err)
	}
	records, err := p.executionStore.ListPlan(persistCtx, claimed.PlanID)
	if err != nil {
		return recorded, err
	}
	_, transitionErr := p.reconcileExecutionStage(snapshot, records)
	return recorded, errors.Join(ErrOperationTimedOut, transitionErr)
}

// finishAction 持久化平台终态，并根据 Plan 内全部 Action 的状态推进 Workflow。
func (p *PlanProcessor) finishAction(ctx context.Context, snapshot workflow.Snapshot, claimed execution.Record, operation platform.Operation, callErr error) (execution.Record, error) {
	status := execution.StatusSucceeded
	message := ""
	if operation.Status != platform.OperationSucceeded {
		status = execution.StatusFailed
		message = strings.TrimSpace(operation.Message)
		if message == "" && callErr != nil {
			message = callErr.Error()
		}
		if message == "" {
			message = "platform operation did not succeed"
		}
	}
	recorded, err := p.executionStore.Complete(ctx, claimed.ActionID, p.workerID,
		operation.ID, string(operation.Status), status, message)
	if err != nil {
		return claimed, fmt.Errorf("complete action execution: %w", err)
	}
	records, err := p.executionStore.ListPlan(ctx, claimed.PlanID)
	if err != nil {
		return recorded, fmt.Errorf("list action executions after completion: %w", err)
	}
	_, transitionErr := p.reconcileExecutionStage(snapshot, records)
	if status == execution.StatusFailed {
		if transitionErr != nil {
			return recorded, transitionErr
		}
		if callErr != nil {
			return recorded, callErr
		}
		return recorded, fmt.Errorf("action %q failed with operation status %q", claimed.ActionID, operation.Status)
	}
	return recorded, transitionErr
}

// reconcileExecutionStage 将 Action 执行状态汇总为修复阶段结果：任一失败即失败，全部成功才完成。
func (p *PlanProcessor) reconcileExecutionStage(snapshot workflow.Snapshot, records []execution.Record) (bool, error) {
	if len(records) == 0 {
		return false, nil
	}
	for _, record := range records {
		if record.Status == execution.StatusFailed {
			if p.workflow.Snapshot().State != workflow.StateRemediating {
				return true, nil
			}
			failure := remediationFailure(record)
			_, err := p.workflow.Apply(workflow.Event{
				Type: workflow.EventStageFailed, Actor: workflow.ActorWorkflow,
				Reason: record.LastError,
				Metadata: map[string]string{
					"plan_id": record.PlanID, "action_id": record.ActionID, "operation_id": record.OperationID,
					"operation_status": record.OperationStatus,
				},
				Failure: &failure,
			})
			return true, err
		}
		if record.Status != execution.StatusSucceeded {
			return false, nil
		}
	}
	if len(records) != len(snapshot.Plan.Actions) {
		return false, nil
	}
	if p.workflow.Snapshot().State != workflow.StateRemediating {
		return true, nil
	}
	_, err := p.workflow.Apply(workflow.Event{
		Type: workflow.EventStageSucceeded, Actor: workflow.ActorWorkflow,
		Reason:   "all plan actions completed successfully",
		Metadata: map[string]string{"plan_id": snapshot.Plan.ID},
	})
	return true, err
}

func (p *PlanProcessor) recordFailure(failure workflow.StageFailure) error {
	_, err := p.workflow.RecordFailure(failure)
	return err
}

func remediationFailure(record execution.Record) workflow.StageFailure {
	value := workflow.StageFailure{
		Stage: workflow.FailureStageRemediation, Message: record.LastError,
		PlanID: record.PlanID, ActionID: record.ActionID, OperationID: record.OperationID,
		OperationStatus: record.OperationStatus, NextAction: workflow.FailureNextReinvestigate,
	}
	switch platform.OperationStatus(record.OperationStatus) {
	case platform.OperationFailed:
		value.Category = workflow.FailureCategoryExecutionFailed
		value.Code = "remediation_operation_failed"
		value.SafeSummary = "修复 Operation 执行失败"
	case platform.OperationRejected:
		value.Category = workflow.FailureCategoryPreconditionChanged
		value.Code = "remediation_operation_rejected"
		value.SafeSummary = "修复 Operation 被平台拒绝，需要重新调查当前状态"
	case platform.OperationCancelled:
		value.Category = workflow.FailureCategoryResultUnknown
		value.Code = "remediation_operation_cancelled"
		value.SafeSummary = "修复 Operation 被取消，需要确认当前资源状态"
	default:
		if record.OperationStatus == "timed_out" {
			value.Category = workflow.FailureCategoryTimedOut
			value.Code = "remediation_operation_timed_out"
			value.SafeSummary = "修复 Operation 超过执行时限"
		} else {
			value.Category = workflow.FailureCategoryUnclassified
			value.Code = "unclassified_remediation_error"
			value.SafeSummary = "修复阶段发生了未分类错误"
			value.Fallback = true
		}
	}
	return value
}

// validateExecutablePlan 确认每个冻结 Action 都有匹配的成功 Dry Run、可执行 Policy 和必要的持久化审批。
func (p *PlanProcessor) validateExecutablePlan(ctx context.Context, snapshot workflow.Snapshot) error {
	if len(snapshot.Plan.Actions) == 0 || len(snapshot.PlanDryRuns) != len(snapshot.Plan.Actions) || len(snapshot.PlanPolicies) != len(snapshot.Plan.Actions) {
		return fmt.Errorf("executable plan requires actions, successful dry runs and policy decisions")
	}
	for _, action := range snapshot.Plan.Actions {
		dryRun := actionDryRun(snapshot.PlanDryRuns, action.ID)
		policy := actionPolicy(snapshot.PlanPolicies, action.ID)
		if dryRun == nil || dryRun.Status != workflow.PlanDryRunSucceeded || dryRun.ActionDigest != action.Digest {
			return fmt.Errorf("action %q does not have a matching successful dry run", action.ID)
		}
		if policy == nil || policy.ActionDigest != action.Digest || policy.Outcome == workflow.PlanPolicyRejected {
			return fmt.Errorf("action %q does not have an executable policy decision", action.ID)
		}
		if policy.Outcome == workflow.PlanPolicyApprovalRequired {
			if p.approvalStore == nil {
				return fmt.Errorf("approval store is required to execute action %q", action.ID)
			}
			persisted, err := p.approvalStore.Get(ctx, approval.RequestID(action.ID))
			if err != nil {
				return fmt.Errorf("read action %q approval: %w", action.ID, err)
			}
			if persisted.Status != approval.StatusApproved || persisted.PlanID != snapshot.Plan.ID || persisted.ActionDigest != action.Digest {
				return fmt.Errorf("action %q does not have a matching approved decision", action.ID)
			}
		}
	}
	return nil
}

// executionSpecs 按 Plan 中的顺序生成不可变执行规格和稳定幂等键。
func executionSpecs(snapshot workflow.Snapshot) []execution.Spec {
	result := make([]execution.Spec, len(snapshot.Plan.Actions))
	for index, action := range snapshot.Plan.Actions {
		result[index] = execution.Spec{
			IncidentID: snapshot.IncidentID, PlanID: snapshot.Plan.ID, ActionID: action.ID,
			ActionDigest: action.Digest, Sequence: index + 1, ToolName: action.ToolName,
			IdempotencyKey: action.ID + ":execute",
		}
	}
	return result
}

// actionPolicy 按 Action ID 查找对应的 Policy 决策。
func actionPolicy(values []workflow.PlanPolicyDecision, actionID string) *workflow.PlanPolicyDecision {
	for index := range values {
		if values[index].ActionID == actionID {
			return &values[index]
		}
	}
	return nil
}

// validatePlatformOperation 校验平台返回的 Operation 确实属于当前冻结 Action 请求。
func validatePlatformOperation(operation platform.Operation, incidentID string, action workflow.PlannedAction, idempotencyKey string) error {
	if strings.TrimSpace(operation.ID) == "" {
		return fmt.Errorf("platform operation ID is required")
	}
	if operation.IncidentID != incidentID || operation.Kind != platform.OperationRemediation ||
		operation.Name != action.ToolName || operation.IdempotencyKey != idempotencyKey {
		return fmt.Errorf("platform operation does not match the claimed action and idempotency key")
	}
	return nil
}

// isTerminalOperation 判断平台 Operation 是否已经得到确定的最终状态。
func isTerminalOperation(status platform.OperationStatus) bool {
	switch status {
	case platform.OperationSucceeded, platform.OperationFailed, platform.OperationRejected, platform.OperationCancelled:
		return true
	default:
		return false
	}
}

// lastExecution 返回列表中的最后一条执行记录；空列表返回零值。
func lastExecution(values []execution.Record) execution.Record {
	if len(values) == 0 {
		return execution.Record{}
	}
	return values[len(values)-1]
}
