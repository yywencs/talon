package workflow

import (
	"fmt"
	"strings"
	"time"
)

// FailureStage 表示结构化失败发生的确定性执行阶段。
type FailureStage string

const (
	FailureStageDryRun             FailureStage = "dry_run"
	FailureStageActionExecution    FailureStage = "action_execution"
	FailureStageArgumentResolution FailureStage = "argument_resolution"
	FailureStageCheckpoint         FailureStage = "checkpoint"
)

// FailureCategory 是跨执行阶段复用的稳定失败分类。
// Controller 和评测器只能依赖该分类或 Code 做分支，不能解析 Message。
type FailureCategory string

const (
	FailureCategoryPlanInvalid           FailureCategory = "plan_invalid"
	FailureCategoryPreconditionChanged   FailureCategory = "precondition_changed"
	FailureCategoryAuthorizationRequired FailureCategory = "authorization_required"
	FailureCategoryExecutionFailed       FailureCategory = "execution_failed"
	FailureCategoryHealthGateFailed      FailureCategory = "health_gate_failed"
	FailureCategoryPlatformUnavailable   FailureCategory = "platform_unavailable"
	FailureCategoryTimedOut              FailureCategory = "timed_out"
	FailureCategoryInvalidResponse       FailureCategory = "invalid_response"
	FailureCategoryResultUnknown         FailureCategory = "result_unknown"
	FailureCategoryUnclassified          FailureCategory = "unclassified"
)

// FailureNextAction 表示 Harness 根据失败事实给出的保守后续动作。
type FailureNextAction string

const (
	FailureNextNeedsAgent FailureNextAction = "needs_agent"
	FailureNextEscalate   FailureNextAction = "escalate"
	FailureNextRetry      FailureNextAction = "retry"
	FailureNextReconcile  FailureNextAction = "reconcile"
)

// StageFailure 是 Dry Run、修复、探测和恢复阶段共用的结构化失败事实。
// SafeSummary 只能包含 Harness 生成的可信摘要；Message 可保留经持久层保护的
// 原始错误用于审计，但不能直接写入模型上下文或参与 Workflow 分支。
type StageFailure struct {
	Stage           FailureStage      `json:"stage"`
	Category        FailureCategory   `json:"category"`
	Code            string            `json:"code"`
	SafeSummary     string            `json:"safe_summary"`
	Message         string            `json:"message,omitempty"`
	NextAction      FailureNextAction `json:"next_action"`
	Retryable       bool              `json:"retryable"`
	Fallback        bool              `json:"fallback,omitempty"`
	PlanID          string            `json:"plan_id,omitempty"`
	ActionID        string            `json:"action_id,omitempty"`
	OperationID     string            `json:"operation_id,omitempty"`
	OperationStatus string            `json:"operation_status,omitempty"`
	WorkflowVersion uint64            `json:"workflow_version"`
	OccurredAt      time.Time         `json:"occurred_at"`
}

func normalizeStageFailure(value StageFailure, now time.Time) (StageFailure, error) {
	value.Code = strings.TrimSpace(value.Code)
	value.SafeSummary = strings.TrimSpace(value.SafeSummary)
	value.Message = strings.TrimSpace(value.Message)
	value.PlanID = strings.TrimSpace(value.PlanID)
	value.ActionID = strings.TrimSpace(value.ActionID)
	value.OperationID = strings.TrimSpace(value.OperationID)
	value.OperationStatus = strings.TrimSpace(value.OperationStatus)
	if !value.Stage.valid() {
		return StageFailure{}, fmt.Errorf("unknown failure stage %q", value.Stage)
	}
	if !value.Category.valid() {
		return StageFailure{}, fmt.Errorf("unknown failure category %q", value.Category)
	}
	if value.Code == "" {
		return StageFailure{}, fmt.Errorf("failure code is required")
	}
	if value.SafeSummary == "" {
		return StageFailure{}, fmt.Errorf("failure safe summary is required")
	}
	if !value.NextAction.valid() {
		return StageFailure{}, fmt.Errorf("unknown failure next action %q", value.NextAction)
	}
	if value.Retryable && value.NextAction != FailureNextRetry {
		return StageFailure{}, fmt.Errorf("retryable failure requires retry next action")
	}
	if value.NextAction == FailureNextRetry && !value.Retryable {
		return StageFailure{}, fmt.Errorf("retry next action requires retryable failure")
	}
	if value.Fallback != (value.Category == FailureCategoryUnclassified) {
		return StageFailure{}, fmt.Errorf("failure fallback does not match category %q", value.Category)
	}
	if value.OccurredAt.IsZero() {
		value.OccurredAt = now
	}
	return value, nil
}

func (s FailureStage) valid() bool {
	switch s {
	case FailureStageDryRun, FailureStageActionExecution,
		FailureStageArgumentResolution, FailureStageCheckpoint:
		return true
	default:
		return false
	}
}

func (c FailureCategory) valid() bool {
	switch c {
	case FailureCategoryPlanInvalid, FailureCategoryPreconditionChanged,
		FailureCategoryAuthorizationRequired, FailureCategoryExecutionFailed,
		FailureCategoryHealthGateFailed, FailureCategoryPlatformUnavailable,
		FailureCategoryTimedOut, FailureCategoryInvalidResponse,
		FailureCategoryResultUnknown, FailureCategoryUnclassified:
		return true
	default:
		return false
	}
}

func (a FailureNextAction) valid() bool {
	switch a {
	case FailureNextNeedsAgent, FailureNextEscalate,
		FailureNextRetry, FailureNextReconcile:
		return true
	default:
		return false
	}
}

func cloneStageFailurePointer(value *StageFailure) *StageFailure {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneStageFailures(values []StageFailure) []StageFailure {
	result := make([]StageFailure, len(values))
	copy(result, values)
	return result
}
