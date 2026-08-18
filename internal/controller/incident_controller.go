package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wen/opentalon/internal/observability"
	"github.com/wen/opentalon/internal/workflow"
)

const defaultMaxControllerAdvances = 32

var (
	// ErrControllerNoProgress 表示被调度组件返回成功，但没有推动 Workflow 状态或版本。
	ErrControllerNoProgress = errors.New("incident controller made no workflow progress")
	// ErrControllerAdvanceLimit 表示单次 Run 超过安全推进次数，防止异常组件造成无限循环。
	ErrControllerAdvanceLimit = errors.New("incident controller advance limit exceeded")
)

// Investigator 是顶层 Controller 对调查 Agent 的最小依赖。
// Agent 必须通过 Workflow 工具提交 Plan 或升级人工，Controller 不解析自然语言输出。
type Investigator interface {
	IncidentID() string
	Investigate(ctx context.Context, instruction string) error
}

// StopReason 说明 IncidentController 为什么把控制权交还给调用方。
type StopReason string

const (
	StopAwaitingApproval StopReason = "awaiting_approval"
	StopCompensating     StopReason = "compensating"
	StopResolved         StopReason = "resolved"
	StopEscalated        StopReason = "escalated"
)

// IncidentControllerConfig 绑定同一个 Incident 的 Agent、Workflow 和确定性执行器。
type IncidentControllerConfig struct {
	Workflow            *workflow.IncidentWorkflow
	Investigator        Investigator
	PlanProcessor       *PlanProcessor
	WorkerRetryInterval time.Duration
	MaxAdvances         int
}

// IncidentRunResult 是一次顶层编排停止时的结构化结果。
type IncidentRunResult struct {
	Reason   StopReason        `json:"reason"`
	Advances int               `json:"advances"`
	Snapshot workflow.Snapshot `json:"snapshot"`
}

// IncidentController 根据 Workflow 状态串联 Agent、Plan 校验、ActionWorker、流量探测和逐级恢复。
// 它不替代状态机，也不绕过各阶段既有的 Policy 与持久化边界。
type IncidentController struct {
	workflow          *workflow.IncidentWorkflow
	investigator      Investigator
	planProcessor     *PlanProcessor
	actionWorker      *ActionWorker
	probeProcessor    *ProbeProcessor
	recoveryProcessor *RecoveryProcessor
	maxAdvances       int
}

// NewIncidentController 创建单 Incident 的顶层编排器。
func NewIncidentController(config IncidentControllerConfig) (*IncidentController, error) {
	if config.Workflow == nil || config.Investigator == nil || config.PlanProcessor == nil {
		return nil, fmt.Errorf("workflow, investigator and plan processor are required")
	}
	if config.PlanProcessor.workflow != config.Workflow {
		return nil, fmt.Errorf("plan processor must use the controller workflow")
	}
	incidentID := strings.TrimSpace(config.Workflow.Snapshot().IncidentID)
	if strings.TrimSpace(config.Investigator.IncidentID()) != incidentID {
		return nil, fmt.Errorf("investigator incident ID does not match workflow")
	}
	retryInterval := config.WorkerRetryInterval
	if retryInterval == 0 {
		retryInterval = time.Second
	}
	worker, err := NewActionWorker(config.PlanProcessor, retryInterval)
	if err != nil {
		return nil, fmt.Errorf("build action worker: %w", err)
	}
	probeProcessor, err := newProbeProcessor(config.PlanProcessor)
	if err != nil {
		return nil, fmt.Errorf("build probe processor: %w", err)
	}
	recoveryProcessor, err := newRecoveryProcessor(config.PlanProcessor)
	if err != nil {
		return nil, fmt.Errorf("build recovery processor: %w", err)
	}
	maxAdvances := config.MaxAdvances
	if maxAdvances == 0 {
		maxAdvances = defaultMaxControllerAdvances
	}
	if maxAdvances < 0 {
		return nil, fmt.Errorf("controller max advances must not be negative")
	}
	return &IncidentController{
		workflow: config.Workflow, investigator: config.Investigator,
		planProcessor: config.PlanProcessor, actionWorker: worker,
		probeProcessor: probeProcessor, recoveryProcessor: recoveryProcessor,
		maxAdvances: maxAdvances,
	}, nil
}

// Run 从当前 checkpoint 持续推进，直到完成、升级人工，或到达需要外部组件接管的阶段。
func (c *IncidentController) Run(ctx context.Context) (IncidentRunResult, error) {
	if c == nil || c.workflow == nil || c.investigator == nil || c.planProcessor == nil ||
		c.actionWorker == nil || c.probeProcessor == nil || c.recoveryProcessor == nil {
		return IncidentRunResult{}, fmt.Errorf("incident controller is not initialized")
	}
	for advances := 0; advances < c.maxAdvances; advances++ {
		if err := ctx.Err(); err != nil {
			return c.result("", advances), err
		}
		before := c.workflow.Snapshot()
		switch before.State {
		case workflow.StateProtected:
			_, err := c.workflow.Apply(workflow.Event{
				Type: workflow.EventStartInvestigation, Actor: workflow.ActorController,
				Reason: "incident controller started investigation",
			})
			if err != nil {
				return c.result("", advances), fmt.Errorf("start incident investigation: %w", err)
			}

		case workflow.StateInvestigating, workflow.StateReinvestigating:
			if err := c.investigator.Investigate(ctx, investigationInstruction(before)); err != nil {
				return c.result("", advances), fmt.Errorf("run incident investigation: %w", err)
			}

		case workflow.StatePlanned:
			if _, err := observability.RunCallback(ctx, "toolops.plan.dry_run", before, c.planProcessor.DryRun); err != nil {
				after := c.workflow.Snapshot()
				if errors.Is(err, ErrPlanDryRunFailed) && after.State != workflow.StatePlanned {
					continue
				}
				return c.result("", advances), fmt.Errorf("dry run incident plan: %w", err)
			}
			if !allPlanDryRunsSucceeded(c.workflow.Snapshot()) {
				return c.result("", advances), fmt.Errorf("%w: plan dry run is still pending", ErrControllerNoProgress)
			}
			if _, err := observability.RunCallback(ctx, "toolops.plan.policy", c.workflow.Snapshot(), c.planProcessor.EvaluatePolicy); err != nil {
				return c.result("", advances), fmt.Errorf("evaluate incident plan policy: %w", err)
			}

		case workflow.StateRemediating:
			_, err := observability.RunCallback(ctx, "toolops.remediation.execute", before, func(ctx context.Context) (workflow.Snapshot, error) {
				err := c.actionWorker.Run(ctx)
				return c.workflow.Snapshot(), err
			})
			if err != nil {
				after := c.workflow.Snapshot()
				if after.State == workflow.StateReinvestigating || after.State == workflow.StateEscalated {
					continue
				}
				return c.result("", advances), fmt.Errorf("run plan actions: %w", err)
			}

		case workflow.StateAwaitingApproval:
			return c.result(StopAwaitingApproval, advances), nil
		case workflow.StateProbing:
			if _, err := observability.RunCallback(ctx, "toolops.probe.run", before, c.probeProcessor.Run); err != nil {
				after := c.workflow.Snapshot()
				if after.State == workflow.StateReinvestigating || after.State == workflow.StateEscalated {
					continue
				}
				return c.result("", advances), fmt.Errorf("run traffic probe: %w", err)
			}
		case workflow.StateRecovering:
			if _, err := observability.RunCallback(ctx, "toolops.recovery.run", before, c.recoveryProcessor.Run); err != nil {
				after := c.workflow.Snapshot()
				if after.State == workflow.StateReinvestigating || after.State == workflow.StateEscalated {
					continue
				}
				return c.result("", advances), fmt.Errorf("run gradual recovery: %w", err)
			}
		case workflow.StateCompensating:
			return c.result(StopCompensating, advances), nil
		case workflow.StateResolved:
			return c.result(StopResolved, advances), nil
		case workflow.StateEscalated:
			return c.result(StopEscalated, advances), nil
		default:
			return c.result("", advances), fmt.Errorf("unsupported workflow state %q", before.State)
		}

		after := c.workflow.Snapshot()
		if after.Version == before.Version && after.State == before.State {
			return c.result("", advances+1), fmt.Errorf("%w in state %q", ErrControllerNoProgress, before.State)
		}
	}
	return c.result("", c.maxAdvances), ErrControllerAdvanceLimit
}

func (c *IncidentController) result(reason StopReason, advances int) IncidentRunResult {
	return IncidentRunResult{Reason: reason, Advances: advances, Snapshot: c.workflow.Snapshot()}
}

func allPlanDryRunsSucceeded(snapshot workflow.Snapshot) bool {
	if snapshot.Plan == nil || len(snapshot.PlanDryRuns) != len(snapshot.Plan.Actions) {
		return false
	}
	for _, result := range snapshot.PlanDryRuns {
		if result.Status != workflow.PlanDryRunSucceeded {
			return false
		}
	}
	return true
}

func investigationInstruction(snapshot workflow.Snapshot) string {
	if snapshot.State != workflow.StateReinvestigating {
		return "调查当前 Incident，收集足够证据后提交安全的结构化 Plan；无法安全处理时升级人工。"
	}
	reason := "上一阶段执行失败"
	metadata := map[string]string(nil)
	if len(snapshot.Failures) > 0 {
		failure := snapshot.Failures[len(snapshot.Failures)-1]
		if strings.TrimSpace(failure.SafeSummary) != "" {
			reason = strings.TrimSpace(failure.SafeSummary)
		}
		metadata = map[string]string{
			"stage": string(failure.Stage), "category": string(failure.Category), "code": failure.Code,
			"next_action": string(failure.NextAction), "operation_id": failure.OperationID,
			"action_id": failure.ActionID, "plan_id": failure.PlanID,
		}
	}
	contextText := reinvestigationContext(metadata)
	return fmt.Sprintf("重新调查当前 Incident。上一阶段失败原因：%s。%s获取新证据并修订根因假设，不要无新证据重复相同修复；随后提交新 Plan 或升级人工。", reason, contextText)
}

func reinvestigationContext(metadata map[string]string) string {
	keys := []string{"stage", "category", "code", "next_action", "operation_id", "action_id", "plan_id"}
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		if value := strings.TrimSpace(metadata[key]); value != "" {
			values = append(values, key+"="+value)
		}
	}
	if len(values) == 0 {
		return ""
	}
	return "关联执行上下文：" + strings.Join(values, "，") + "。"
}
