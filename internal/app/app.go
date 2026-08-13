// Package app 组装可执行的 Talon ToolOps 应用链路。
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/wen/opentalon/internal/agent"
	"github.com/wen/opentalon/internal/controller"
	"github.com/wen/opentalon/internal/observability"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/scenario"
	"github.com/wen/opentalon/internal/simulator"
	"github.com/wen/opentalon/internal/storage"
	"github.com/wen/opentalon/internal/workflow"
)

const defaultScenarioID = "mapping-regression-rollback-001"

// Config 定义一次独立、完整的 ToolOps 场景运行。
type Config struct {
	DatasetRoot         string
	ScenarioID          string
	Model               model.ToolCallingChatModel
	InvestigatorFactory func(*workflow.IncidentWorkflow, platform.ToolOpsPlatform) (controller.Investigator, error)
	Storage             *storage.Storage
	Output              io.Writer
	AutoApprove         bool
	AgentMaxSteps       int
	ClockPollInterval   time.Duration
	WorkerRetryInterval time.Duration
}

// Result 汇总一次场景运行结束时的 Workflow 与 Simulator 状态。
type Result struct {
	Controller controller.IncidentRunResult
	World      simulator.Snapshot
}

// Run 从数据集异常事件开始，运行到 resolved、escalated 或审批门禁。
func Run(ctx context.Context, cfg Config) (result Result, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(cfg.DatasetRoot) == "" {
		return Result{}, fmt.Errorf("dataset root is required")
	}
	if cfg.Storage == nil {
		return Result{}, fmt.Errorf("application storage is required")
	}
	if cfg.Output == nil {
		cfg.Output = io.Discard
	}
	if cfg.ScenarioID == "" {
		cfg.ScenarioID = defaultScenarioID
	}
	if cfg.ClockPollInterval <= 0 {
		cfg.ClockPollInterval = 20 * time.Millisecond
	}
	if cfg.WorkerRetryInterval <= 0 {
		cfg.WorkerRetryInterval = 20 * time.Millisecond
	}

	dataset, err := scenario.LoadDataset(cfg.DatasetRoot)
	if err != nil {
		return Result{}, fmt.Errorf("load scenario dataset: %w", err)
	}
	item, ok := dataset.Find(strings.TrimSpace(cfg.ScenarioID))
	if !ok {
		return Result{}, fmt.Errorf("scenario %q was not found", cfg.ScenarioID)
	}
	service, err := simulator.New(item.Scenario)
	if err != nil {
		return Result{}, fmt.Errorf("create scenario simulator: %w", err)
	}
	incidentAt, err := firstTimelineEvent(item.Scenario)
	if err != nil {
		return Result{}, err
	}
	if err := service.Advance(ctx, incidentAt); err != nil {
		return Result{}, fmt.Errorf("advance simulator to incident: %w", err)
	}

	printer := &safePrinter{writer: cfg.Output}
	printer.printf("[talon] scenario=%s title=%s\n", item.Scenario.Metadata.ID, item.Scenario.Metadata.Title)
	printer.printf("[simulator] advanced_to=%s incident_after=%s\n", service.Snapshot().Now.Format(time.RFC3339), incidentAt)

	flow, err := workflow.NewIncidentWorkflow(workflow.Config{IncidentID: item.Scenario.Metadata.ID})
	if err != nil {
		return Result{}, fmt.Errorf("create incident workflow: %w", err)
	}
	ctx, finishTrace := observability.BeginCallback(ctx, "toolops.incident.run", map[string]any{
		"scenario_id": item.Scenario.Metadata.ID,
		"title":       item.Scenario.Metadata.Title,
	})
	if traceID := observability.TraceIDFromContext(ctx); traceID != "" {
		printer.printf("[trace] trace_id=%s\n", traceID)
	}
	defer func() {
		finishTrace(result.Controller, err)
	}()

	var investigator controller.Investigator
	if cfg.InvestigatorFactory != nil {
		investigator, err = cfg.InvestigatorFactory(flow, service)
		if err != nil {
			return Result{}, fmt.Errorf("create scenario investigator: %w", err)
		}
	} else {
		if cfg.Model == nil {
			return Result{}, fmt.Errorf("model is required when investigator factory is not provided")
		}
		toolOpsAgent, buildErr := agent.NewToolOpsAgent(ctx, agent.Config{
			Model: cfg.Model, Platform: service, IncidentID: item.Scenario.Metadata.ID,
			Workflow: flow, MaxSteps: cfg.AgentMaxSteps,
		})
		if buildErr != nil {
			return Result{}, fmt.Errorf("create ToolOps Agent: %w", buildErr)
		}
		investigator = &printingInvestigator{agent: toolOpsAgent, printer: printer}
	}
	if investigator.IncidentID() != item.Scenario.Metadata.ID {
		return Result{}, fmt.Errorf("investigator incident ID does not match scenario")
	}

	processor, err := controller.NewPlanProcessor(service, flow,
		controller.WithApprovalStore(cfg.Storage.Approvals()),
		controller.WithExecutionStore(cfg.Storage.Executions(), item.Scenario.Metadata.ID+"-scenario-worker", 5*time.Second),
		controller.WithAsyncExecution(controller.AsyncExecutionConfig{
			SubmitTimeout: 5 * time.Second, InitialPollInterval: 20 * time.Millisecond,
			MaxPollInterval: 100 * time.Millisecond, OperationTimeout: 2 * time.Minute,
		}),
	)
	if err != nil {
		return Result{}, fmt.Errorf("create plan processor: %w", err)
	}
	orchestrator, err := controller.NewIncidentController(controller.IncidentControllerConfig{
		Workflow: flow, Investigator: investigator, PlanProcessor: processor,
		WorkerRetryInterval: cfg.WorkerRetryInterval,
	})
	if err != nil {
		return Result{}, fmt.Errorf("create incident controller: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	clockErrors := make(chan error, 1)
	go driveSimulatorClock(runCtx, cancel, service, cfg.ClockPollInterval, printer, clockErrors)

	seenVersion := uint64(0)
	for {
		runResult, runErr := orchestrator.Run(runCtx)
		printTransitions(printer, runResult.Snapshot, &seenVersion)
		if clockErr := receiveClockError(clockErrors); clockErr != nil {
			return Result{Controller: runResult, World: service.Snapshot()}, clockErr
		}
		if runErr != nil {
			return Result{Controller: runResult, World: service.Snapshot()}, runErr
		}
		if runResult.Reason != controller.StopAwaitingApproval {
			result := Result{Controller: runResult, World: service.Snapshot()}
			printSummary(printer, result)
			return result, nil
		}
		if !cfg.AutoApprove {
			printer.printf("[approval] waiting for human decision; enable automatic approval only for an isolated Simulator run\n")
			result := Result{Controller: runResult, World: service.Snapshot()}
			printSummary(printer, result)
			return result, nil
		}
		_, approvalErr := observability.RunCallback(runCtx, "toolops.plan.approval", runResult.Snapshot,
			func(callbackCtx context.Context) (workflow.Snapshot, error) {
				err := approvePendingActions(callbackCtx, processor, runResult.Snapshot.IncidentID, printer)
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

func approvePendingActions(ctx context.Context, processor *controller.PlanProcessor, incidentID string, printer *safePrinter) error {
	requests, err := processor.ListPendingApprovals(ctx)
	if err != nil {
		return fmt.Errorf("list scenario approvals: %w", err)
	}
	approved := 0
	for _, request := range requests {
		if request.IncidentID != incidentID {
			continue
		}
		printer.printf("[approval] SIMULATOR AUTO-APPROVE action=%s tool=%s risk=%s digest=%s\n",
			request.ActionID, request.ToolName, request.Risk, request.ActionDigest)
		_, err := processor.Approve(ctx, controller.ApprovalRequest{
			PlanID: request.PlanID, ActionID: request.ActionID, ActionDigest: request.ActionDigest,
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

func printTransitions(printer *safePrinter, snapshot workflow.Snapshot, seen *uint64) {
	for _, transition := range snapshot.History {
		if transition.Version <= *seen {
			continue
		}
		printer.printf("[workflow] v%d %s -> %s event=%s actor=%s reason=%s\n",
			transition.Version, transition.From, transition.To, transition.Event, transition.Actor, transition.Reason)
		*seen = transition.Version
	}
}

func printSummary(printer *safePrinter, result Result) {
	snapshot := result.Controller.Snapshot
	printer.printf("[result] reason=%s state=%s advances=%d transitions=%d\n",
		result.Controller.Reason, snapshot.State, result.Controller.Advances, len(snapshot.History))
	if snapshot.Plan != nil {
		printer.printf("[plan] id=%s root_cause=%s actions=%d probe_route=%s recovery_policy=%s\n",
			snapshot.Plan.ID, snapshot.Plan.RootCause, len(snapshot.Plan.Actions),
			snapshot.Plan.ProbeRouteID, snapshot.Plan.RecoveryPolicyID)
	}
	routeIDs := make([]string, 0, len(result.World.Routes))
	for id := range result.World.Routes {
		routeIDs = append(routeIDs, id)
	}
	sort.Strings(routeIDs)
	for _, id := range routeIDs {
		route := result.World.Routes[id]
		printer.printf("[route] id=%s weight=%d baseline=%d enabled=%t\n", id, route.Weight, route.BaselineWeight, route.Enabled)
	}
}

func receiveClockError(values <-chan error) error {
	select {
	case err := <-values:
		return err
	default:
		return nil
	}
}

func mapText(values map[string]any, key string) string {
	if values == nil || values[key] == nil {
		return "-"
	}
	encoded, err := json.Marshal(values[key])
	if err != nil {
		return "?"
	}
	return strings.Trim(string(encoded), `"`)
}

type printingInvestigator struct {
	agent   *agent.ToolOpsAgent
	printer *safePrinter
}

func (p *printingInvestigator) IncidentID() string { return p.agent.IncidentID() }

func (p *printingInvestigator) Investigate(ctx context.Context, instruction string) error {
	p.printer.printf("[agent] instruction=%s\n", instruction)
	message, err := p.agent.Run(ctx, instruction)
	if err != nil {
		return err
	}
	if message != nil && strings.TrimSpace(message.Content) != "" {
		p.printer.printf("[agent] response:\n%s\n", message.Content)
	}
	return nil
}

type safePrinter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (p *safePrinter) printf(format string, values ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = fmt.Fprintf(p.writer, format, values...)
}

var _ controller.Investigator = (*printingInvestigator)(nil)
