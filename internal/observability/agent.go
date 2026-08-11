package observability

import (
	"context"
	"strings"
	"time"

	cozeloop "github.com/coze-dev/cozeloop-go"
	"github.com/coze-dev/cozeloop-go/spec/tracespec"
)

const (
	agentSpanType           = "agent"
	attrIncidentID          = "toolops.incident.id"
	attrAgentOperation      = "toolops.agent.operation"
	attrWorkflowInitial     = "toolops.workflow.initial_state"
	attrWorkflowFinal       = "toolops.workflow.final_state"
	attrWorkflowTransitions = "toolops.workflow.transitions"
)

// WorkflowTransition 是写入 Agent Trace 的最小状态迁移信息。
type WorkflowTransition struct {
	From    string    `json:"from"`
	To      string    `json:"to"`
	Event   string    `json:"event"`
	Actor   string    `json:"actor"`
	Version uint64    `json:"version"`
	At      time.Time `json:"at,omitempty"`
}

// AgentRun 保存一次 ToolOps Agent 根 Span 及其安全配置。
type AgentRun struct {
	ctx      context.Context
	span     cozeloop.Span
	redactor *Redactor
}

// StartAgentRun 创建 CozeLoop agent 根 Span；Eino Callback 会自动成为其子 Span。
func StartAgentRun(ctx context.Context, incidentID, operation, initialState string, input any) (context.Context, *AgentRun) {
	if ctx == nil {
		ctx = context.Background()
	}
	client, cfg, redactor := providerSnapshot()
	if client == nil {
		return ctx, nil
	}
	ctx, span := client.StartSpan(ctx, "toolops.agent.run", agentSpanType)
	span.SetServiceName(ctx, cfg.ServiceName)
	span.SetDeploymentEnv(ctx, cfg.DeploymentEnv)
	span.SetThreadID(ctx, strings.TrimSpace(incidentID))
	span.SetInput(ctx, safeTraceValue(redactor, tracespec.Input, input))
	span.SetTags(ctx, map[string]any{
		attrIncidentID:      strings.TrimSpace(incidentID),
		attrAgentOperation:  strings.TrimSpace(operation),
		attrWorkflowInitial: strings.TrimSpace(initialState),
		"agent_name":        "ToolOpsAgent",
	})
	return ctx, &AgentRun{ctx: ctx, span: span, redactor: redactor}
}

// FinishAgentRun 写入最终状态、迁移记录、输出或错误，并结束根 Span。
func FinishAgentRun(run *AgentRun, err error, finalState string, transitions []WorkflowTransition, output any) {
	if run == nil || run.span == nil {
		return
	}
	if output != nil {
		run.span.SetOutput(run.ctx, safeTraceValue(run.redactor, tracespec.Output, output))
	}
	run.span.SetTags(run.ctx, map[string]any{
		attrWorkflowFinal:       strings.TrimSpace(finalState),
		attrWorkflowTransitions: safeTraceValue(run.redactor, attrWorkflowTransitions, transitions),
	})
	if err != nil {
		run.span.SetError(run.ctx, safeTraceError(run.redactor, err))
		run.span.SetStatusCode(run.ctx, tracespec.VErrDefault)
	}
	run.span.Finish(run.ctx)
}
