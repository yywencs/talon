package controller

import (
	"context"
	"errors"

	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/workflow"
)

// classifyControllerError 只识别可以由稳定 error identity 判断的错误。
// 未识别错误采用 fail-closed 的 unclassified/escalate，禁止解析错误字符串猜测语义。
func classifyControllerError(stage workflow.FailureStage, code, safeSummary, planID, actionID string,
	operation platform.Operation, err error,
) workflow.StageFailure {
	value := workflow.StageFailure{
		Stage: stage, Code: code, SafeSummary: safeSummary, Message: errorMessage(err),
		PlanID: planID, ActionID: actionID, OperationID: operation.ID,
		OperationStatus: string(operation.Status),
	}
	switch {
	case errors.Is(err, platform.ErrUnauthorized):
		value.Category = workflow.FailureCategoryAuthorizationRequired
		value.NextAction = workflow.FailureNextEscalate
	case errors.Is(err, platform.ErrConflict), errors.Is(err, platform.ErrPreconditionFailed):
		value.Category = workflow.FailureCategoryPreconditionChanged
		value.NextAction = workflow.FailureNextNeedsAgent
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		value.Category = workflow.FailureCategoryPlatformUnavailable
		value.NextAction = workflow.FailureNextRetry
		value.Retryable = true
	case errors.Is(err, platform.ErrUnsupported):
		value.Category = workflow.FailureCategoryInvalidResponse
		value.NextAction = workflow.FailureNextEscalate
	default:
		value.Category = workflow.FailureCategoryUnclassified
		value.NextAction = workflow.FailureNextEscalate
		value.Fallback = true
	}
	return value
}

func invalidResponseFailure(stage workflow.FailureStage, code, safeSummary, planID, actionID string,
	operation platform.Operation,
) workflow.StageFailure {
	return workflow.StageFailure{
		Stage: stage, Category: workflow.FailureCategoryInvalidResponse, Code: code,
		SafeSummary: safeSummary, NextAction: workflow.FailureNextEscalate,
		PlanID: planID, ActionID: actionID, OperationID: operation.ID,
		OperationStatus: string(operation.Status),
	}
}

func unknownResultFailure(stage workflow.FailureStage, code, safeSummary, planID, actionID string,
	operation platform.Operation, err error,
) workflow.StageFailure {
	return workflow.StageFailure{
		Stage: stage, Category: workflow.FailureCategoryResultUnknown, Code: code,
		SafeSummary: safeSummary, Message: errorMessage(err), NextAction: workflow.FailureNextReconcile,
		PlanID: planID, ActionID: actionID, OperationID: operation.ID,
		OperationStatus: string(operation.Status),
	}
}

func errorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
