// Package controller 编排 Workflow 与受管平台之间的确定性执行步骤。
package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/workflow"
)

var ErrPlanDryRunFailed = errors.New("plan dry run failed")

// PlanProcessor 负责执行已经由 Workflow 冻结的 Plan，不让 Agent 直接调用生产写操作。
type PlanProcessor struct {
	platform platform.ToolOpsPlatform
	workflow *workflow.IncidentWorkflow
}

// NewPlanProcessor 创建 Plan 执行编排器。
func NewPlanProcessor(service platform.ToolOpsPlatform, instance *workflow.IncidentWorkflow) (*PlanProcessor, error) {
	if service == nil {
		return nil, fmt.Errorf("toolops platform is required")
	}
	if instance == nil {
		return nil, fmt.Errorf("incident workflow is required")
	}
	return &PlanProcessor{platform: service, workflow: instance}, nil
}

// DryRun 按顺序对当前冻结 Plan 的每个 Action 做无副作用预执行。
// 每个结果分别绑定 Action ID 和摘要；已完成的 Action 不会被重复调用。
func (p *PlanProcessor) DryRun(ctx context.Context) ([]workflow.PlanDryRun, error) {
	if p == nil || p.platform == nil || p.workflow == nil {
		return nil, fmt.Errorf("plan processor is not initialized")
	}
	snapshot := p.workflow.Snapshot()
	for _, result := range snapshot.PlanDryRuns {
		if result.Status == workflow.PlanDryRunFailed {
			return snapshot.PlanDryRuns, ErrPlanDryRunFailed
		}
	}
	if snapshot.State != workflow.StatePlanned {
		return nil, fmt.Errorf("%w: plan dry run is not allowed in state %q", workflow.ErrInvalidTransition, snapshot.State)
	}
	if snapshot.Plan == nil {
		return nil, fmt.Errorf("planned workflow has no frozen plan")
	}

	for _, action := range snapshot.Plan.Actions {
		if terminalActionDryRun(snapshot.PlanDryRuns, action.ID) {
			continue
		}
		request := remediationDryRunRequest(snapshot.IncidentID, action)
		operation, callErr := p.platform.ExecuteRemediation(ctx, request)
		status, failure := analyzeDryRunResult(operation, callErr)
		result := workflow.PlanDryRun{
			PlanID: snapshot.Plan.ID, ActionID: action.ID, ActionDigest: action.Digest,
			OperationID: operation.ID, IdempotencyKey: request.IdempotencyKey,
			Status: status, OperationStatus: string(operation.Status), Message: operation.Message,
			Failure: failure, Result: cloneMap(operation.Result),
		}
		recorded, recordErr := p.workflow.RecordPlanDryRun(result)
		if recordErr != nil {
			return p.workflow.Snapshot().PlanDryRuns, fmt.Errorf("record action %q dry run: %w", action.ID, recordErr)
		}
		if callErr != nil {
			if recorded.Status == workflow.PlanDryRunFailed {
				return p.workflow.Snapshot().PlanDryRuns, errors.Join(ErrPlanDryRunFailed, callErr)
			}
			return p.workflow.Snapshot().PlanDryRuns, fmt.Errorf("execute action %q dry run: %w", action.ID, callErr)
		}
		if recorded.Status == workflow.PlanDryRunFailed {
			return p.workflow.Snapshot().PlanDryRuns, fmt.Errorf("%w: action %q operation status %q", ErrPlanDryRunFailed, action.ID, recorded.OperationStatus)
		}
	}
	return p.workflow.Snapshot().PlanDryRuns, nil
}

func remediationDryRunRequest(incidentID string, action workflow.PlannedAction) platform.RemediationRequest {
	arguments := cloneMap(action.Arguments)
	expectedVersion, _ := arguments["expected_version"].(string)
	delete(arguments, "idempotency_key")
	delete(arguments, "expected_version")
	delete(arguments, "dry_run")
	return platform.RemediationRequest{
		IncidentID: strings.TrimSpace(incidentID), ToolName: action.ToolName,
		Arguments: arguments, ExpectedVersion: strings.TrimSpace(expectedVersion), DryRun: true,
		IdempotencyKey: action.ID + ":dry-run",
	}
}

func terminalActionDryRun(values []workflow.PlanDryRun, actionID string) bool {
	for _, value := range values {
		if value.ActionID == actionID {
			return value.Status == workflow.PlanDryRunSucceeded || value.Status == workflow.PlanDryRunFailed
		}
	}
	return false
}

func analyzeDryRunResult(operation platform.Operation, err error) (workflow.PlanDryRunStatus, *workflow.PlanDryRunFailure) {
	if err != nil {
		message := strings.TrimSpace(operation.Message)
		if message == "" {
			message = err.Error()
		}
		switch {
		case errors.Is(err, platform.ErrNotFound):
			return failedPlanDryRun(workflow.PlanDryRunFailurePlanInvalid, "capability_not_found", message, workflow.PlanDryRunNextReplan)
		case errors.Is(err, platform.ErrUnsupported):
			return failedPlanDryRun(workflow.PlanDryRunFailurePlanInvalid, "capability_unsupported", message, workflow.PlanDryRunNextReplan)
		case errors.Is(err, platform.ErrUnauthorized):
			return failedPlanDryRun(workflow.PlanDryRunFailureAuthorizationRequired, "authorization_denied", message, workflow.PlanDryRunNextEscalate)
		case errors.Is(err, platform.ErrConflict):
			return failedPlanDryRun(workflow.PlanDryRunFailurePreconditionChanged, "state_conflict", message, workflow.PlanDryRunNextReinvestigate)
		case errors.Is(err, platform.ErrPreconditionFailed):
			return failedPlanDryRun(workflow.PlanDryRunFailurePreconditionChanged, "precondition_failed", message, workflow.PlanDryRunNextReinvestigate)
		default:
			return workflow.PlanDryRunIndeterminate, &workflow.PlanDryRunFailure{
				Category: workflow.PlanDryRunFailurePlatformUnavailable, Code: "platform_unavailable",
				Message: message, NextAction: workflow.PlanDryRunNextRetry, Retryable: true,
			}
		}
	}
	message := strings.TrimSpace(operation.Message)
	switch operation.Status {
	case platform.OperationPending, platform.OperationRunning:
		return workflow.PlanDryRunPending, nil
	case platform.OperationSucceeded:
		return workflow.PlanDryRunSucceeded, nil
	case platform.OperationRejected:
		return failedPlanDryRun(workflow.PlanDryRunFailurePlanInvalid, "operation_rejected", message, workflow.PlanDryRunNextReplan)
	case platform.OperationFailed:
		return failedPlanDryRun(workflow.PlanDryRunFailureExecutionFailed, "operation_failed", message, workflow.PlanDryRunNextReinvestigate)
	case platform.OperationCancelled:
		return workflow.PlanDryRunIndeterminate, &workflow.PlanDryRunFailure{
			Category: workflow.PlanDryRunFailurePlatformUnavailable, Code: "operation_cancelled",
			Message: message, NextAction: workflow.PlanDryRunNextRetry, Retryable: true,
		}
	default:
		return workflow.PlanDryRunIndeterminate, &workflow.PlanDryRunFailure{
			Category: workflow.PlanDryRunFailurePlatformUnavailable, Code: "invalid_operation_status",
			Message: message, NextAction: workflow.PlanDryRunNextRetry, Retryable: true,
		}
	}
}

func failedPlanDryRun(category workflow.PlanDryRunFailureCategory, code, message string, next workflow.PlanDryRunNextAction) (workflow.PlanDryRunStatus, *workflow.PlanDryRunFailure) {
	return workflow.PlanDryRunFailed, &workflow.PlanDryRunFailure{
		Category: category, Code: code, Message: message, NextAction: next,
	}
}

func cloneMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = cloneValue(item)
	}
	return result
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneValue(item)
		}
		return result
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}
