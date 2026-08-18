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
	StopResolved         StopReason = "resolved"
	StopEscalated        StopReason = "escalated"
	StopFailed           StopReason = "failed"
	StopBlocked          StopReason = "blocked"
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

// IncidentController 根据 Workflow 状态串联 Agent、Plan 校验、ActionWorker 和 Checkpoint。
// 它不替代状态机，也不绕过各阶段既有的 Policy 与持久化边界。
type IncidentController struct {
	workflow      *workflow.IncidentWorkflow
	investigator  Investigator
	planProcessor *PlanProcessor
	actionWorker  *ActionWorker
	maxAdvances   int
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
		maxAdvances: maxAdvances,
	}, nil
}

// Run 从当前 checkpoint 持续推进，直到完成、升级人工，或到达需要外部组件接管的阶段。
func (c *IncidentController) Run(ctx context.Context) (IncidentRunResult, error) {
	if c == nil || c.workflow == nil || c.investigator == nil || c.planProcessor == nil ||
		c.actionWorker == nil {
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

		case workflow.StateInvestigating:
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
				if after.State == workflow.StateInvestigating || after.State == workflow.StateEscalated ||
					after.State == workflow.StateFailed || after.State == workflow.StateBlocked {
					continue
				}
				return c.result("", advances), fmt.Errorf("run plan actions: %w", err)
			}

		case workflow.StateAwaitingApproval:
			if err := c.planProcessor.persistCheckpoint(ctx); err != nil {
				return c.result("", advances), fmt.Errorf("persist awaiting approval checkpoint: %w", err)
			}
			return c.result(StopAwaitingApproval, advances), nil
		case workflow.StateResolved:
			if err := c.planProcessor.persistCheckpoint(ctx); err != nil {
				return c.result("", advances), fmt.Errorf("persist resolved checkpoint: %w", err)
			}
			return c.result(StopResolved, advances), nil
		case workflow.StateEscalated:
			if err := c.planProcessor.persistCheckpoint(ctx); err != nil {
				return c.result("", advances), fmt.Errorf("persist escalated checkpoint: %w", err)
			}
			return c.result(StopEscalated, advances), nil
		case workflow.StateCheckpoint:
			if _, err := c.workflow.EvaluateCheckpoint(); err != nil {
				return c.result("", advances), fmt.Errorf("evaluate decision checkpoint: %w", err)
			}
		case workflow.StateFailed:
			if err := c.planProcessor.persistCheckpoint(ctx); err != nil {
				return c.result("", advances), fmt.Errorf("persist failed checkpoint: %w", err)
			}
			return c.result(StopFailed, advances), nil
		case workflow.StateBlocked:
			if err := c.planProcessor.persistCheckpoint(ctx); err != nil {
				return c.result("", advances), fmt.Errorf("persist blocked checkpoint: %w", err)
			}
			return c.result(StopBlocked, advances), nil
		default:
			return c.result("", advances), fmt.Errorf("unsupported workflow state %q", before.State)
		}

		after := c.workflow.Snapshot()
		if err := c.planProcessor.persistCheckpoint(ctx); err != nil {
			return c.result("", advances+1), fmt.Errorf("persist workflow checkpoint: %w", err)
		}
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
	actions := workflow.ExecutableActions(snapshot)
	if snapshot.Plan == nil || len(actions) == 0 || len(snapshot.PlanDryRuns) != len(actions) {
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
	if len(snapshot.Checkpoints) == 0 || snapshot.Checkpoints[len(snapshot.Checkpoints)-1].Decision != workflow.CheckpointNeedsAgent {
		return "调查当前 Incident，收集足够证据后提交安全的结构化 Plan；无法安全处理时升级人工。"
	}
	checkpoint := snapshot.Checkpoints[len(snapshot.Checkpoints)-1]
	planID := ""
	if snapshot.Plan != nil {
		planID = snapshot.Plan.ID
	}
	reason := strings.TrimSpace(checkpoint.DecisionReason)
	metadata := map[string]string{
		"stage": checkpoint.StageID, "code": string(checkpoint.Decision), "plan_id": planID,
		"evidence_refs": strings.Join(checkpoint.NewEvidenceRefs, ","),
	}
	failed := checkpoint.Trigger == "stage_failed" || checkpoint.Trigger == "dry_run" || checkpoint.Trigger == "argument_resolution"
	if failed && len(snapshot.Failures) > 0 {
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
	contextText := agentResumeContext(metadata)
	if failed {
		return fmt.Sprintf("继续处理当前 Incident。上一执行阶段未成功：%s。%s基于当前证据决定下一步；不要无新证据重复相同动作，随后提交新的短 Stage Plan 或升级人工。", reason, contextText)
	}
	return fmt.Sprintf("继续处理当前 Incident。上一 Stage 已完成，Checkpoint 需要 Agent 结合新观察做语义判断：%s。%s基于当前证据提交新的短 Stage Plan、确认完成，或升级人工。", reason, contextText)
}

func agentResumeContext(metadata map[string]string) string {
	keys := []string{"stage", "category", "code", "next_action", "operation_id", "action_id", "plan_id", "evidence_refs"}
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
