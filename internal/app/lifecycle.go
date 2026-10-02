package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wen/opentalon/internal/controller"
	"github.com/wen/opentalon/internal/observability"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/scenario"
	"github.com/wen/opentalon/internal/simulator"
	"github.com/wen/opentalon/internal/workflow"
)

// advanceRun owns the simulator clock and approval loop. Joining the clock is
// required before returning control to tracing and final audit capture.
func advanceRun(ctx context.Context, cfg Config, flow *workflow.IncidentWorkflow, service *simulator.Simulator, orchestrator *controller.IncidentController, processor *controller.ExecutionCoordinator, printer *safePrinter) (result Result, err error) {
	runCtx, cancel := context.WithCancel(ctx)
	clockErrors := make(chan error, 1)
	clockDone := make(chan struct{})
	go func() {
		defer close(clockDone)
		driveSimulatorClock(runCtx, cancel, service, cfg.ClockPollInterval, printer, clockErrors)
	}()
	defer func() {
		cancel()
		<-clockDone
		result.World = service.Snapshot()
		select {
		case clockErr := <-clockErrors:
			err = errors.Join(clockErr, err)
		default:
		}
	}()
	seenVersion := uint64(0)
	for {
		runResult, runErr := orchestrator.Run(runCtx)
		printTransitions(printer, runResult.Snapshot, &seenVersion)
		if runErr != nil {
			return Result{Controller: runResult, World: service.Snapshot()}, runErr
		}
		if runResult.Reason != controller.StopAwaitingApproval {
			result := Result{Controller: runResult, World: service.Snapshot()}
			return result, nil
		}
		if !cfg.AutoApprove {
			printer.printf("[approval] waiting for human decision; enable automatic approval only for an isolated Simulator run\n")
			result := Result{Controller: runResult, World: service.Snapshot()}
			return result, nil
		}
		_, approvalErr := observability.RunCallback(runCtx, "toolops.intent.approval", runResult.Snapshot,
			func(callbackCtx context.Context) (workflow.Snapshot, error) {
				err := approvePendingActions(callbackCtx, processor, runResult.Snapshot, printer)
				return flow.Snapshot(), err
			})
		if approvalErr != nil {
			return Result{Controller: runResult, World: service.Snapshot()}, approvalErr
		}
	}
}

func firstTimelineEvent(document scenario.Scenario) (time.Duration, error) {
	if len(document.Timeline) == 0 {
		return 0, fmt.Errorf("scenario %q has no incident timeline event", document.Metadata.ID)
	}
	var selected time.Duration
	for index, event := range document.Timeline {
		value, err := time.ParseDuration(event.At)
		if err != nil {
			return 0, fmt.Errorf("parse scenario timeline event %d: %w", index, err)
		}
		if value < 0 {
			return 0, fmt.Errorf("scenario timeline event %d must not be negative", index)
		}
		if index == 0 || value < selected {
			selected = value
		}
	}
	return selected, nil
}

func agentStartOffset(document scenario.Scenario) (time.Duration, error) {
	if value := strings.TrimSpace(document.Clock.IncidentAt); value != "" {
		incidentAt, err := time.ParseDuration(value)
		if err != nil {
			return 0, fmt.Errorf("parse scenario clock.incident_at: %w", err)
		}
		return incidentAt, nil
	}
	return firstTimelineEvent(document)
}

// driveSimulatorClock 只在存在活动异步 Operation 时推进虚拟时间。
// 这样 LLM 调查和人工审批等待不会消耗场景时间。
func driveSimulatorClock(ctx context.Context, cancel context.CancelFunc, service *simulator.Simulator, interval time.Duration, printer *safePrinter, errorsOut chan<- error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snapshot := service.Snapshot()
			active := activeOperations(snapshot.Operations)
			if len(active) == 0 {
				continue
			}
			if err := service.Advance(ctx, snapshot.Tick); err != nil {
				if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
					return
				}
				select {
				case errorsOut <- fmt.Errorf("advance simulator clock: %w", err):
				default:
				}
				cancel()
				return
			}
			current := service.Snapshot()
			for _, id := range active {
				operation := current.Operations[id]
				printer.printf("[operation] virtual_time=%s id=%s kind=%s status=%s outcome=%s step=%s weight=%s\n",
					current.Now.Format(time.RFC3339), id, operation.Kind, operation.Status,
					mapText(operation.Result, "outcome"), mapText(operation.Result, "current_step"), mapText(operation.Result, "route_weight"))
			}
		}
	}
}

func activeOperations(values map[string]platform.Operation) []string {
	result := make([]string, 0)
	for id, operation := range values {
		if operation.Status == platform.OperationPending || operation.Status == platform.OperationRunning {
			result = append(result, id)
		}
	}
	sort.Strings(result)
	return result
}

func approvePendingActions(ctx context.Context, processor *controller.ExecutionCoordinator, snapshot workflow.Snapshot, printer *safePrinter) error {
	if snapshot.ExecutionIntent == nil {
		return fmt.Errorf("workflow awaits approval without a frozen intent")
	}
	actions := make(map[string]string)
	for _, action := range workflow.ExecutableActions(snapshot) {
		actions[action.ID] = action.Digest
	}
	requests, err := processor.ListPendingApprovals(ctx)
	if err != nil {
		return fmt.Errorf("list scenario approvals: %w", err)
	}
	approved := 0
	for _, request := range requests {
		if request.IncidentID != snapshot.IncidentID || request.IntentID != snapshot.ExecutionIntent.ID {
			continue
		}
		if digest, ok := actions[request.ActionID]; !ok || digest != request.ActionDigest {
			continue
		}
		printer.printf("[approval] SIMULATOR AUTO-APPROVE action=%s tool=%s risk=%s digest=%s\n",
			request.ActionID, request.ToolName, request.Risk, request.ActionDigest)
		_, err := processor.Approve(ctx, controller.ApprovalRequest{
			IntentID: request.IntentID, ActionID: request.ActionID, ActionDigest: request.ActionDigest,
			Approver: "talon-scenario-runner", Reason: "explicit automatic approval for isolated Simulator execution",
		})
		if err != nil {
			return fmt.Errorf("approve scenario action %q: %w", request.ActionID, err)
		}
		approved++
	}
	if approved == 0 {
		return fmt.Errorf("workflow awaits approval but no pending action was found")
	}
	return nil
}
