package workflow

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ActionDryRunStatus 表示 ExecutionIntent 预执行检查的归一化结果。
type ActionDryRunStatus string

const (
	// ActionDryRunPending 表示平台已接收 Dry Run，但结果尚未确定。
	ActionDryRunPending ActionDryRunStatus = "pending"
	// ActionDryRunSucceeded 表示参数和前置条件检查通过，且没有修改平台状态。
	ActionDryRunSucceeded ActionDryRunStatus = "succeeded"
	// ActionDryRunIndeterminate 表示平台暂时不可用等原因导致检查结果未知，可以安全重试。
	ActionDryRunIndeterminate ActionDryRunStatus = "indeterminate"
	// ActionDryRunFailed 表示 Dry Run 已被确定性拒绝或失败。
	ActionDryRunFailed ActionDryRunStatus = "failed"
)

// ActionDryRunFailureCategory 表示 Dry Run 失败的稳定业务分类，禁止依赖错误文本判断分支。
type ActionDryRunFailureCategory string

const (
	ActionDryRunFailureIntentInvalid         ActionDryRunFailureCategory = "intent_invalid"
	ActionDryRunFailurePreconditionChanged   ActionDryRunFailureCategory = "precondition_changed"
	ActionDryRunFailureAuthorizationRequired ActionDryRunFailureCategory = "authorization_required"
	ActionDryRunFailureExecutionFailed       ActionDryRunFailureCategory = "execution_failed"
	ActionDryRunFailurePlatformUnavailable   ActionDryRunFailureCategory = "platform_unavailable"
	ActionDryRunFailureInvalidResponse       ActionDryRunFailureCategory = "invalid_response"
	ActionDryRunFailureUnclassified          ActionDryRunFailureCategory = "unclassified"
)

// ActionDryRunNextAction 是 Controller 根据失败分类给出的确定性后续动作。
type ActionDryRunNextAction string

const (
	ActionDryRunNextNeedsAgent ActionDryRunNextAction = "needs_agent"
	ActionDryRunNextEscalate   ActionDryRunNextAction = "escalate"
	ActionDryRunNextRetry      ActionDryRunNextAction = "retry"
)

// ActionDryRunFailure 保存供 Workflow、Agent 和审计使用的结构化失败原因。
type ActionDryRunFailure struct {
	Category   ActionDryRunFailureCategory `json:"category"`
	Code       string                      `json:"code"`
	Message    string                      `json:"message"`
	NextAction ActionDryRunNextAction      `json:"next_action"`
	Retryable  bool                        `json:"retryable"`
}

// ActionDryRun 保存一次冻结 ExecutionIntent 的预执行结果。
// OperationStatus 保留平台原始状态，Status 则供 Workflow 做确定性判断。
type ActionDryRun struct {
	IntentID        string               `json:"intent_id"`
	ActionID        string               `json:"action_id"`
	ActionDigest    string               `json:"action_digest"`
	OperationID     string               `json:"operation_id,omitempty"`
	IdempotencyKey  string               `json:"idempotency_key"`
	Status          ActionDryRunStatus   `json:"status"`
	OperationStatus string               `json:"operation_status,omitempty"`
	Message         string               `json:"message,omitempty"`
	Failure         *ActionDryRunFailure `json:"failure,omitempty"`
	Result          map[string]any       `json:"result,omitempty"`
	RecordedAt      time.Time            `json:"recorded_at"`
}

// RecordActionDryRun 原子保存当前 ExecutionIntent 的 Dry Run 结果。
// 成功、等待或可重试检查保持 validating；确定性失败按 NextAction 唤回 Agent 或升级。
func (w *IncidentWorkflow) RecordActionDryRun(result ActionDryRun) (ActionDryRun, error) {
	if w == nil {
		return ActionDryRun{}, fmt.Errorf("workflow is not initialized")
	}
	result.IntentID = strings.TrimSpace(result.IntentID)
	result.ActionID = strings.TrimSpace(result.ActionID)
	result.ActionDigest = strings.TrimSpace(result.ActionDigest)
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
	if result.IntentID == "" {
		return ActionDryRun{}, fmt.Errorf("intent dry run intent_id is required")
	}
	if result.ActionID == "" {
		return ActionDryRun{}, fmt.Errorf("intent dry run action_id is required")
	}
	if result.ActionDigest == "" {
		return ActionDryRun{}, fmt.Errorf("intent dry run action_digest is required")
	}
	if result.IdempotencyKey == "" {
		return ActionDryRun{}, fmt.Errorf("intent dry run idempotency_key is required")
	}
	if !result.Status.valid() {
		return ActionDryRun{}, fmt.Errorf("unknown intent dry run status %q", result.Status)
	}
	if err := validateActionDryRunFailure(result.Status, result.Failure); err != nil {
		return ActionDryRun{}, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if existing := findActionDryRun(w.actionDryRuns, result.ActionID); existing != nil &&
		existing.IntentID == result.IntentID && existing.IdempotencyKey == result.IdempotencyKey && existing.Status.terminal() {
		return *cloneActionDryRunPointer(existing), nil
	}
	if w.state != StateValidating {
		return ActionDryRun{}, fmt.Errorf("%w: intent dry run is not allowed in state %q", ErrInvalidTransition, w.state)
	}
	if w.intent == nil || w.intent.ID != result.IntentID {
		return ActionDryRun{}, fmt.Errorf("intent dry run does not match the current frozen intent")
	}
	action := findIntendedAction(w.executableActionsLocked(), result.ActionID)
	if action == nil || action.Digest != result.ActionDigest {
		return ActionDryRun{}, fmt.Errorf("intent dry run does not match a frozen action")
	}

	result.RecordedAt = w.now()
	w.actionDryRuns = upsertActionDryRun(w.actionDryRuns, result)
	w.allActionDryRuns = upsertActionDryRun(w.allActionDryRuns, result)
	if result.Failure != nil {
		reason := result.Failure.Message
		if reason == "" {
			reason = result.Message
		}
		if reason == "" {
			reason = "intent dry run did not succeed"
		}
		metadata := map[string]string{
			"intent_id":           result.IntentID,
			"action_id":           result.ActionID,
			"action_digest":       result.ActionDigest,
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
		failure := normalizedDryRunFailure(result)
		switch result.Failure.NextAction {
		case ActionDryRunNextNeedsAgent:
			if stage := w.currentStageLocked(); stage != nil {
				decision, eventType, checkpointReason := w.needsAgentDecisionLocked(dryRunSafeSummary(failure.Category))
				checkpoint := w.newCheckpointLocked(stage.StageID, "dry_run", decision, checkpointReason, "")
				w.checkpoints = append(w.checkpoints, checkpoint)
				if _, err := w.applyLocked(Event{Type: eventType, Actor: ActorWorkflow, Reason: reason,
					Metadata: map[string]string{"intent_id": result.IntentID, "stage_id": stage.StageID, "checkpoint_id": checkpoint.CheckpointID}, Failure: &failure}); err != nil {
					return ActionDryRun{}, err
				}
				break
			}
			if _, err := w.applyLocked(Event{
				Type: EventExecutionIntentRejected, Actor: ActorWorkflow, Reason: reason, Metadata: metadata, Failure: &failure,
			}); err != nil {
				return ActionDryRun{}, err
			}
		case ActionDryRunNextEscalate:
			if stage := w.currentStageLocked(); stage != nil {
				checkpoint := w.newCheckpointLocked(stage.StageID, "dry_run", CheckpointBlocked, dryRunSafeSummary(failure.Category), "")
				w.checkpoints = append(w.checkpoints, checkpoint)
				if _, err := w.applyLocked(Event{Type: EventCheckpointBlocked, Actor: ActorWorkflow, Reason: reason,
					Metadata: map[string]string{"intent_id": result.IntentID, "stage_id": stage.StageID, "checkpoint_id": checkpoint.CheckpointID}, Failure: &failure}); err != nil {
					return ActionDryRun{}, err
				}
				break
			}
			if _, err := w.applyLocked(Event{
				Type: EventEscalated, Actor: ActorWorkflow, Reason: reason, Metadata: metadata, Failure: &failure,
			}); err != nil {
				return ActionDryRun{}, err
			}
		case ActionDryRunNextRetry:
			// 暂时性失败保持 validating，由 Controller 使用相同幂等键安全重试。
			failure.WorkflowVersion = w.version
			normalized, err := normalizeStageFailure(failure, w.now())
			if err != nil {
				return ActionDryRun{}, err
			}
			w.failures = append(w.failures, normalized)
		}
	}
	return *cloneActionDryRunPointer(findActionDryRun(w.actionDryRuns, result.ActionID)), nil
}

func normalizedDryRunFailure(result ActionDryRun) StageFailure {
	failure := result.Failure
	category := map[ActionDryRunFailureCategory]FailureCategory{
		ActionDryRunFailureIntentInvalid:         FailureCategoryIntentInvalid,
		ActionDryRunFailurePreconditionChanged:   FailureCategoryPreconditionChanged,
		ActionDryRunFailureAuthorizationRequired: FailureCategoryAuthorizationRequired,
		ActionDryRunFailureExecutionFailed:       FailureCategoryExecutionFailed,
		ActionDryRunFailurePlatformUnavailable:   FailureCategoryPlatformUnavailable,
		ActionDryRunFailureInvalidResponse:       FailureCategoryInvalidResponse,
		ActionDryRunFailureUnclassified:          FailureCategoryUnclassified,
	}[failure.Category]
	next := map[ActionDryRunNextAction]FailureNextAction{
		ActionDryRunNextNeedsAgent: FailureNextNeedsAgent,
		ActionDryRunNextEscalate:   FailureNextEscalate,
		ActionDryRunNextRetry:      FailureNextRetry,
	}[failure.NextAction]
	return StageFailure{
		Stage: FailureStageDryRun, Category: category, Code: failure.Code,
		SafeSummary: dryRunSafeSummary(category), Message: failure.Message,
		NextAction: next, Retryable: failure.Retryable,
		Fallback: failure.Category == ActionDryRunFailureUnclassified,
		IntentID: result.IntentID, ActionID: result.ActionID, OperationID: result.OperationID,
		OperationStatus: result.OperationStatus,
	}
}

func dryRunSafeSummary(category FailureCategory) string {
	switch category {
	case FailureCategoryIntentInvalid:
		return "ExecutionIntent 的动作或参数未通过 Dry Run 校验"
	case FailureCategoryPreconditionChanged:
		return "执行前置条件已发生变化，需要 Agent 重新决策"
	case FailureCategoryAuthorizationRequired:
		return "Dry Run 所需操作未获得授权"
	case FailureCategoryExecutionFailed:
		return "Dry Run 平台操作执行失败"
	case FailureCategoryPlatformUnavailable:
		return "Dry Run 暂时无法从平台获得确定结果"
	case FailureCategoryInvalidResponse:
		return "Dry Run 平台返回了无法识别的响应"
	default:
		return "Dry Run 发生了未分类错误"
	}
}

func (s ActionDryRunStatus) valid() bool {
	switch s {
	case ActionDryRunPending, ActionDryRunSucceeded, ActionDryRunIndeterminate, ActionDryRunFailed:
		return true
	default:
		return false
	}
}

func (s ActionDryRunStatus) terminal() bool {
	return s == ActionDryRunSucceeded || s == ActionDryRunFailed
}

func validateActionDryRunFailure(status ActionDryRunStatus, failure *ActionDryRunFailure) error {
	if status == ActionDryRunPending || status == ActionDryRunSucceeded {
		if failure != nil {
			return fmt.Errorf("intent dry run status %q must not contain failure", status)
		}
		return nil
	}
	if failure == nil {
		return fmt.Errorf("intent dry run status %q requires structured failure", status)
	}
	if !failure.Category.valid() {
		return fmt.Errorf("unknown intent dry run failure category %q", failure.Category)
	}
	if strings.TrimSpace(failure.Code) == "" {
		return fmt.Errorf("intent dry run failure code is required")
	}
	if !failure.NextAction.valid() {
		return fmt.Errorf("unknown intent dry run next action %q", failure.NextAction)
	}
	if status == ActionDryRunIndeterminate && (failure.NextAction != ActionDryRunNextRetry || !failure.Retryable) {
		return fmt.Errorf("indeterminate intent dry run must be retryable")
	}
	if status == ActionDryRunFailed && failure.NextAction == ActionDryRunNextRetry {
		return fmt.Errorf("failed intent dry run cannot use retry next action")
	}
	expectedAction := map[ActionDryRunFailureCategory]ActionDryRunNextAction{
		ActionDryRunFailureIntentInvalid:         ActionDryRunNextNeedsAgent,
		ActionDryRunFailurePreconditionChanged:   ActionDryRunNextNeedsAgent,
		ActionDryRunFailureAuthorizationRequired: ActionDryRunNextEscalate,
		ActionDryRunFailureExecutionFailed:       ActionDryRunNextNeedsAgent,
		ActionDryRunFailurePlatformUnavailable:   ActionDryRunNextRetry,
		ActionDryRunFailureInvalidResponse:       ActionDryRunNextEscalate,
		ActionDryRunFailureUnclassified:          ActionDryRunNextEscalate,
	}[failure.Category]
	if failure.NextAction != expectedAction {
		return fmt.Errorf("intent dry run failure category %q requires next action %q", failure.Category, expectedAction)
	}
	if failure.Retryable != (failure.Category == ActionDryRunFailurePlatformUnavailable) {
		return fmt.Errorf("intent dry run failure retryable does not match category %q", failure.Category)
	}
	return nil
}

func (c ActionDryRunFailureCategory) valid() bool {
	switch c {
	case ActionDryRunFailureIntentInvalid, ActionDryRunFailurePreconditionChanged,
		ActionDryRunFailureAuthorizationRequired, ActionDryRunFailureExecutionFailed,
		ActionDryRunFailurePlatformUnavailable, ActionDryRunFailureInvalidResponse,
		ActionDryRunFailureUnclassified:
		return true
	default:
		return false
	}
}

func (a ActionDryRunNextAction) valid() bool {
	switch a {
	case ActionDryRunNextNeedsAgent, ActionDryRunNextEscalate, ActionDryRunNextRetry:
		return true
	default:
		return false
	}
}

func cloneActionDryRunPointer(value *ActionDryRun) *ActionDryRun {
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

func cloneActionDryRuns(values []ActionDryRun) []ActionDryRun {
	result := make([]ActionDryRun, len(values))
	for index := range values {
		result[index] = *cloneActionDryRunPointer(&values[index])
	}
	return result
}

func findActionDryRun(values []ActionDryRun, actionID string) *ActionDryRun {
	for index := range values {
		if values[index].ActionID == actionID {
			return &values[index]
		}
	}
	return nil
}

func upsertActionDryRun(values []ActionDryRun, value ActionDryRun) []ActionDryRun {
	for index := range values {
		if values[index].ActionID == value.ActionID {
			values[index] = *cloneActionDryRunPointer(&value)
			return values
		}
	}
	return append(values, *cloneActionDryRunPointer(&value))
}

func findIntendedAction(values []IntendedAction, actionID string) *IntendedAction {
	for index := range values {
		if values[index].ID == actionID {
			return &values[index]
		}
	}
	return nil
}
