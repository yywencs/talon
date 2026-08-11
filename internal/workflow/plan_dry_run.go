package workflow

import (
	"fmt"
	"strings"
	"time"
)

// PlanDryRunStatus 表示 Plan 预执行检查的归一化结果。
type PlanDryRunStatus string

const (
	// PlanDryRunPending 表示平台已接收 Dry Run，但结果尚未确定。
	PlanDryRunPending PlanDryRunStatus = "pending"
	// PlanDryRunSucceeded 表示参数和前置条件检查通过，且没有修改平台状态。
	PlanDryRunSucceeded PlanDryRunStatus = "succeeded"
	// PlanDryRunIndeterminate 表示平台暂时不可用等原因导致检查结果未知，可以安全重试。
	PlanDryRunIndeterminate PlanDryRunStatus = "indeterminate"
	// PlanDryRunFailed 表示 Dry Run 被拒绝、失败或无法安全完成。
	PlanDryRunFailed PlanDryRunStatus = "failed"
)

// PlanDryRun 保存一次冻结 Plan 的预执行结果。
// OperationStatus 保留平台原始状态，Status 则供 Workflow 做确定性判断。
type PlanDryRun struct {
	PlanID          string           `json:"plan_id"`
	OperationID     string           `json:"operation_id,omitempty"`
	IdempotencyKey  string           `json:"idempotency_key"`
	Status          PlanDryRunStatus `json:"status"`
	OperationStatus string           `json:"operation_status,omitempty"`
	Message         string           `json:"message,omitempty"`
	Error           string           `json:"error,omitempty"`
	Result          map[string]any   `json:"result,omitempty"`
	RecordedAt      time.Time        `json:"recorded_at"`
}

// RecordPlanDryRun 原子保存当前 Plan 的 Dry Run 结果。
// 成功或等待中的检查保持 planned；失败的检查会拒绝 Plan 并回到 investigating。
func (w *IncidentWorkflow) RecordPlanDryRun(result PlanDryRun) (PlanDryRun, error) {
	if w == nil {
		return PlanDryRun{}, fmt.Errorf("workflow is not initialized")
	}
	result.PlanID = strings.TrimSpace(result.PlanID)
	result.OperationID = strings.TrimSpace(result.OperationID)
	result.IdempotencyKey = strings.TrimSpace(result.IdempotencyKey)
	result.OperationStatus = strings.TrimSpace(result.OperationStatus)
	result.Message = strings.TrimSpace(result.Message)
	result.Error = strings.TrimSpace(result.Error)
	if result.PlanID == "" {
		return PlanDryRun{}, fmt.Errorf("plan dry run plan_id is required")
	}
	if result.IdempotencyKey == "" {
		return PlanDryRun{}, fmt.Errorf("plan dry run idempotency_key is required")
	}
	if !result.Status.valid() {
		return PlanDryRun{}, fmt.Errorf("unknown plan dry run status %q", result.Status)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.planDryRun != nil && w.planDryRun.PlanID == result.PlanID &&
		w.planDryRun.IdempotencyKey == result.IdempotencyKey && w.planDryRun.Status.terminal() {
		return *clonePlanDryRunPointer(w.planDryRun), nil
	}
	if w.state != StatePlanned {
		return PlanDryRun{}, fmt.Errorf("%w: plan dry run is not allowed in state %q", ErrInvalidTransition, w.state)
	}
	if w.plan == nil || w.plan.ID != result.PlanID {
		return PlanDryRun{}, fmt.Errorf("plan dry run does not match the current frozen plan")
	}

	result.RecordedAt = w.now()
	w.planDryRun = clonePlanDryRunPointer(&result)
	if result.Status == PlanDryRunFailed {
		reason := result.Error
		if reason == "" {
			reason = result.Message
		}
		if reason == "" {
			reason = "plan dry run failed"
		}
		metadata := map[string]string{
			"plan_id":        result.PlanID,
			"dry_run_status": string(result.Status),
		}
		if result.OperationID != "" {
			metadata["operation_id"] = result.OperationID
		}
		if result.OperationStatus != "" {
			metadata["operation_status"] = result.OperationStatus
		}
		if _, err := w.applyLocked(Event{
			Type: EventPlanRejected, Actor: ActorWorkflow, Reason: reason, Metadata: metadata,
		}); err != nil {
			return PlanDryRun{}, err
		}
	}
	return *clonePlanDryRunPointer(w.planDryRun), nil
}

func (s PlanDryRunStatus) valid() bool {
	switch s {
	case PlanDryRunPending, PlanDryRunSucceeded, PlanDryRunIndeterminate, PlanDryRunFailed:
		return true
	default:
		return false
	}
}

func (s PlanDryRunStatus) terminal() bool {
	return s == PlanDryRunSucceeded || s == PlanDryRunFailed
}

func clonePlanDryRunPointer(value *PlanDryRun) *PlanDryRun {
	if value == nil {
		return nil
	}
	result := *value
	result.Result = cloneAnyMap(value.Result)
	return &result
}
