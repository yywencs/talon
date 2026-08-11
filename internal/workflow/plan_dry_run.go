package workflow

import (
	"fmt"
	"strconv"
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
	// PlanDryRunFailed 表示 Dry Run 已被确定性拒绝或失败。
	PlanDryRunFailed PlanDryRunStatus = "failed"
)

// PlanDryRunFailureCategory 表示 Dry Run 失败的稳定业务分类，禁止依赖错误文本判断分支。
type PlanDryRunFailureCategory string

const (
	PlanDryRunFailurePlanInvalid           PlanDryRunFailureCategory = "plan_invalid"
	PlanDryRunFailurePreconditionChanged   PlanDryRunFailureCategory = "precondition_changed"
	PlanDryRunFailureAuthorizationRequired PlanDryRunFailureCategory = "authorization_required"
	PlanDryRunFailureExecutionFailed       PlanDryRunFailureCategory = "execution_failed"
	PlanDryRunFailurePlatformUnavailable   PlanDryRunFailureCategory = "platform_unavailable"
)

// PlanDryRunNextAction 是 Controller 根据失败分类给出的确定性后续动作。
type PlanDryRunNextAction string

const (
	PlanDryRunNextReplan        PlanDryRunNextAction = "replan"
	PlanDryRunNextReinvestigate PlanDryRunNextAction = "reinvestigate"
	PlanDryRunNextEscalate      PlanDryRunNextAction = "escalate"
	PlanDryRunNextRetry         PlanDryRunNextAction = "retry"
)

// PlanDryRunFailure 保存供 Workflow、Agent 和审计使用的结构化失败原因。
type PlanDryRunFailure struct {
	Category   PlanDryRunFailureCategory `json:"category"`
	Code       string                    `json:"code"`
	Message    string                    `json:"message"`
	NextAction PlanDryRunNextAction      `json:"next_action"`
	Retryable  bool                      `json:"retryable"`
}

// PlanDryRun 保存一次冻结 Plan 的预执行结果。
// OperationStatus 保留平台原始状态，Status 则供 Workflow 做确定性判断。
type PlanDryRun struct {
	PlanID          string             `json:"plan_id"`
	OperationID     string             `json:"operation_id,omitempty"`
	IdempotencyKey  string             `json:"idempotency_key"`
	Status          PlanDryRunStatus   `json:"status"`
	OperationStatus string             `json:"operation_status,omitempty"`
	Message         string             `json:"message,omitempty"`
	Failure         *PlanDryRunFailure `json:"failure,omitempty"`
	Result          map[string]any     `json:"result,omitempty"`
	RecordedAt      time.Time          `json:"recorded_at"`
}

// RecordPlanDryRun 原子保存当前 Plan 的 Dry Run 结果。
// 成功、等待或可重试检查保持 planned；确定性失败按 NextAction 重新规划、调查或升级。
func (w *IncidentWorkflow) RecordPlanDryRun(result PlanDryRun) (PlanDryRun, error) {
	if w == nil {
		return PlanDryRun{}, fmt.Errorf("workflow is not initialized")
	}
	result.PlanID = strings.TrimSpace(result.PlanID)
	result.OperationID = strings.TrimSpace(result.OperationID)
	result.IdempotencyKey = strings.TrimSpace(result.IdempotencyKey)
	result.OperationStatus = strings.TrimSpace(result.OperationStatus)
	result.Message = strings.TrimSpace(result.Message)
	if result.Failure != nil {
		failure := *result.Failure
		failure.Code = strings.TrimSpace(failure.Code)
		failure.Message = strings.TrimSpace(failure.Message)
		result.Failure = &failure
	}
	if result.PlanID == "" {
		return PlanDryRun{}, fmt.Errorf("plan dry run plan_id is required")
	}
	if result.IdempotencyKey == "" {
		return PlanDryRun{}, fmt.Errorf("plan dry run idempotency_key is required")
	}
	if !result.Status.valid() {
		return PlanDryRun{}, fmt.Errorf("unknown plan dry run status %q", result.Status)
	}
	if err := validatePlanDryRunFailure(result.Status, result.Failure); err != nil {
		return PlanDryRun{}, err
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
	if result.Failure != nil {
		reason := result.Failure.Message
		if reason == "" {
			reason = result.Message
		}
		if reason == "" {
			reason = "plan dry run did not succeed"
		}
		metadata := map[string]string{
			"plan_id":             result.PlanID,
			"dry_run_status":      string(result.Status),
			"failure_category":    string(result.Failure.Category),
			"failure_code":        result.Failure.Code,
			"failure_next_action": string(result.Failure.NextAction),
			"failure_retryable":   strconv.FormatBool(result.Failure.Retryable),
		}
		if result.OperationID != "" {
			metadata["operation_id"] = result.OperationID
		}
		if result.OperationStatus != "" {
			metadata["operation_status"] = result.OperationStatus
		}
		switch result.Failure.NextAction {
		case PlanDryRunNextReplan, PlanDryRunNextReinvestigate:
			if _, err := w.applyLocked(Event{
				Type: EventPlanRejected, Actor: ActorWorkflow, Reason: reason, Metadata: metadata,
			}); err != nil {
				return PlanDryRun{}, err
			}
		case PlanDryRunNextEscalate:
			if _, err := w.applyLocked(Event{
				Type: EventEscalated, Actor: ActorWorkflow, Reason: reason, Metadata: metadata,
			}); err != nil {
				return PlanDryRun{}, err
			}
		case PlanDryRunNextRetry:
			// 暂时性失败保持 planned，由 Controller 使用相同幂等键安全重试。
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

func validatePlanDryRunFailure(status PlanDryRunStatus, failure *PlanDryRunFailure) error {
	if status == PlanDryRunPending || status == PlanDryRunSucceeded {
		if failure != nil {
			return fmt.Errorf("plan dry run status %q must not contain failure", status)
		}
		return nil
	}
	if failure == nil {
		return fmt.Errorf("plan dry run status %q requires structured failure", status)
	}
	if !failure.Category.valid() {
		return fmt.Errorf("unknown plan dry run failure category %q", failure.Category)
	}
	if strings.TrimSpace(failure.Code) == "" {
		return fmt.Errorf("plan dry run failure code is required")
	}
	if !failure.NextAction.valid() {
		return fmt.Errorf("unknown plan dry run next action %q", failure.NextAction)
	}
	if status == PlanDryRunIndeterminate && (failure.NextAction != PlanDryRunNextRetry || !failure.Retryable) {
		return fmt.Errorf("indeterminate plan dry run must be retryable")
	}
	if status == PlanDryRunFailed && failure.NextAction == PlanDryRunNextRetry {
		return fmt.Errorf("failed plan dry run cannot use retry next action")
	}
	expectedAction := map[PlanDryRunFailureCategory]PlanDryRunNextAction{
		PlanDryRunFailurePlanInvalid:           PlanDryRunNextReplan,
		PlanDryRunFailurePreconditionChanged:   PlanDryRunNextReinvestigate,
		PlanDryRunFailureAuthorizationRequired: PlanDryRunNextEscalate,
		PlanDryRunFailureExecutionFailed:       PlanDryRunNextReinvestigate,
		PlanDryRunFailurePlatformUnavailable:   PlanDryRunNextRetry,
	}[failure.Category]
	if failure.NextAction != expectedAction {
		return fmt.Errorf("plan dry run failure category %q requires next action %q", failure.Category, expectedAction)
	}
	if failure.Retryable != (failure.Category == PlanDryRunFailurePlatformUnavailable) {
		return fmt.Errorf("plan dry run failure retryable does not match category %q", failure.Category)
	}
	return nil
}

func (c PlanDryRunFailureCategory) valid() bool {
	switch c {
	case PlanDryRunFailurePlanInvalid, PlanDryRunFailurePreconditionChanged,
		PlanDryRunFailureAuthorizationRequired, PlanDryRunFailureExecutionFailed,
		PlanDryRunFailurePlatformUnavailable:
		return true
	default:
		return false
	}
}

func (a PlanDryRunNextAction) valid() bool {
	switch a {
	case PlanDryRunNextReplan, PlanDryRunNextReinvestigate, PlanDryRunNextEscalate, PlanDryRunNextRetry:
		return true
	default:
		return false
	}
}

func clonePlanDryRunPointer(value *PlanDryRun) *PlanDryRun {
	if value == nil {
		return nil
	}
	result := *value
	result.Result = cloneAnyMap(value.Result)
	if value.Failure != nil {
		failure := *value.Failure
		result.Failure = &failure
	}
	return &result
}
