// Package agent 提供 Talon 的 ToolOps Agent 实现。
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	flowagent "github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	"github.com/wen/opentalon/internal/observability"
	"github.com/wen/opentalon/internal/platform"
	toolset "github.com/wen/opentalon/internal/tools"
	"github.com/wen/opentalon/internal/workflow"
)

const (
	// DefaultMaxSteps 限制一次 Agent 调用最多执行的 Eino Graph 节点数，
	// 防止模型在查询或工具失败时无限循环。
	DefaultMaxSteps = 24
	graphName       = "ToolOpsAgent"
)

// Config 定义一个绑定到单个 Incident 的 ToolOpsAgent。
// Model 和 Platform 由外层注入，因此 Agent 不依赖具体模型供应商或真实平台。
type Config struct {
	Model      model.ToolCallingChatModel
	Platform   platform.ToolOpsPlatform
	IncidentID string
	MaxSteps   int
	Workflow   *workflow.IncidentWorkflow

	// AdditionalInstructions 只能补充当前部署环境的调查说明，
	// 固定的安全边界始终由 Agent 内置提示词和 Platform 共同保证。
	AdditionalInstructions string
}

// ToolOpsAgent 使用 Eino ReAct 循环调查 Incident，并调用受控的 ToolOps 工具。
// 每个实例只绑定一个 Incident，避免模型跨事件查询或执行动作。
type ToolOpsAgent struct {
	incidentID string
	systemText string
	tools      *toolset.Set
	workflow   *workflow.IncidentWorkflow
	runner     *react.Agent
}

// NewToolOpsAgent 创建一个可嵌入后续 IncidentWorkflow 的单 Agent。
func NewToolOpsAgent(ctx context.Context, config Config) (*ToolOpsAgent, error) {
	if config.Model == nil {
		return nil, fmt.Errorf("tool calling model is required")
	}
	if config.Platform == nil {
		return nil, fmt.Errorf("toolops platform is required")
	}
	if config.Workflow == nil {
		return nil, fmt.Errorf("incident workflow is required")
	}
	incidentID := strings.TrimSpace(config.IncidentID)
	if incidentID == "" {
		return nil, fmt.Errorf("incident ID is required")
	}
	if config.MaxSteps < 0 {
		return nil, fmt.Errorf("max steps must not be negative")
	}
	maxSteps := config.MaxSteps
	if maxSteps == 0 {
		maxSteps = DefaultMaxSteps
	}

	observedPlatform := observability.ObservePlatform(config.Platform)
	tools, err := toolset.New(ctx, observedPlatform, incidentID, toolset.WithWorkflow(config.Workflow))
	if err != nil {
		return nil, fmt.Errorf("build incident tools: %w", err)
	}

	visibleTools := tools.ToolsForActions(config.Workflow.AllowedAgentActions())
	persona := systemPrompt(incidentID, config.AdditionalInstructions)
	runner, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: config.Model,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools:               visibleTools,
			ExecuteSequentially: true,
			ToolCallMiddlewares: []compose.ToolMiddleware{workflowToolGuard(config.Workflow, tools)},
			UnknownToolsHandler: func(_ context.Context, name, _ string) (string, error) {
				result, marshalErr := json.Marshal(map[string]any{
					"data":  nil,
					"error": fmt.Sprintf("工具 %s 不存在，只能调用当前已注册的工具", name),
				})
				return string(result), marshalErr
			},
		},
		MaxStep:       maxSteps,
		GraphName:     graphName,
		ModelNodeName: "ToolOpsModel",
		ToolsNodeName: "ToolOpsTools",
	})
	if err != nil {
		return nil, fmt.Errorf("build ToolOps ReAct agent: %w", err)
	}

	return &ToolOpsAgent{
		incidentID: incidentID, systemText: persona, tools: tools,
		workflow: config.Workflow, runner: runner,
	}, nil
}

// IncidentID 返回当前 Agent 被授权处理的唯一 Incident。
func (a *ToolOpsAgent) IncidentID() string {
	if a == nil {
		return ""
	}
	return a.incidentID
}

// Run 使用一条工作流指令启动一次 Agent 调用。
// instruction 为空时，Agent 默认开始调查当前 Incident。
func (a *ToolOpsAgent) Run(ctx context.Context, instruction string, opts ...flowagent.AgentOption) (*schema.Message, error) {
	if a == nil || a.runner == nil {
		return nil, fmt.Errorf("ToolOpsAgent is not initialized")
	}
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		instruction = defaultInstruction
	}
	messages := []*schema.Message{
		schema.SystemMessage(a.currentSystemText()),
		schema.UserMessage(instruction),
	}
	options, err := a.withWorkflowTools(ctx, opts)
	if err != nil {
		return nil, err
	}
	return a.runner.Generate(ctx, messages, options...)
}

// Generate 使用已有消息继续一次 Agent 调用，供后续工作流恢复上下文时使用。
func (a *ToolOpsAgent) Generate(ctx context.Context, messages []*schema.Message, opts ...flowagent.AgentOption) (*schema.Message, error) {
	if a == nil || a.runner == nil {
		return nil, fmt.Errorf("ToolOpsAgent is not initialized")
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("agent messages are required")
	}
	options, err := a.withWorkflowTools(ctx, opts)
	if err != nil {
		return nil, err
	}
	return a.runner.Generate(ctx, a.withSystemMessage(messages), options...)
}

// Stream 以流式方式运行 Agent，主要用于命令行或管理界面展示。
func (a *ToolOpsAgent) Stream(ctx context.Context, messages []*schema.Message, opts ...flowagent.AgentOption) (*schema.StreamReader[*schema.Message], error) {
	if a == nil || a.runner == nil {
		return nil, fmt.Errorf("ToolOpsAgent is not initialized")
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("agent messages are required")
	}
	options, err := a.withWorkflowTools(ctx, opts)
	if err != nil {
		return nil, err
	}
	return a.runner.Stream(ctx, a.withSystemMessage(messages), options...)
}

func (a *ToolOpsAgent) withWorkflowTools(ctx context.Context, options []flowagent.AgentOption) ([]flowagent.AgentOption, error) {
	visible := a.tools.ToolsForActions(a.workflow.AllowedAgentActions())
	policyOptions, err := react.WithTools(ctx, visible...)
	if err != nil {
		return nil, fmt.Errorf("build workflow tool options: %w", err)
	}
	tracingOption := flowagent.WithComposeOptions(compose.WithCallbacks(observability.NewEinoTracingHandler()))
	result := make([]flowagent.AgentOption, 0, len(options)+len(policyOptions)+1)
	result = append(result, options...)
	result = append(result, tracingOption)
	return append(result, policyOptions...), nil
}

// workflowToolGuard 在每次工具真正执行前重新读取 Workflow 状态并校验 AgentAction。
// 模型在一轮 ReAct 开始时拿到的工具列表可能因 submit_plan 或 escalate 等调用而过期，
// 因此不能只依赖“模型是否看得见工具”；未分类或当前状态已禁止的工具会返回结构化拒绝结果。
// escalate_incident 成功后，Guard 还负责提交 EventEscalated，使平台操作和状态机保持一致。
func workflowToolGuard(instance *workflow.IncidentWorkflow, tools *toolset.Set) compose.ToolMiddleware {
	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				action, classified := tools.AgentAction(input.Name)
				if !classified {
					return deniedToolOutput(fmt.Errorf("tool %q is not available to the Agent workflow", input.Name))
				}
				if err := instance.AuthorizeAgentAction(action); err != nil {
					return deniedToolOutput(err)
				}
				output, err := next(ctx, input)
				if err != nil {
					return nil, err
				}
				if action == workflow.AgentActionEscalate && toolResponseSucceeded(output.Result) {
					if _, applyErr := instance.Apply(workflow.Event{Type: workflow.EventEscalated, Actor: workflow.ActorAgent}); applyErr != nil {
						return nil, fmt.Errorf("apply escalation event: %w", applyErr)
					}
				}
				return output, nil
			}
		},
	}
}

func deniedToolOutput(err error) (*compose.ToolOutput, error) {
	result, marshalErr := json.Marshal(map[string]any{"data": nil, "error": err.Error()})
	if marshalErr != nil {
		return nil, marshalErr
	}
	return &compose.ToolOutput{Result: string(result)}, nil
}

func toolResponseSucceeded(value string) bool {
	var result struct {
		Error string `json:"error"`
	}
	return json.Unmarshal([]byte(value), &result) == nil && result.Error == ""
}

// withSystemMessage 按照 Eino ReAct 的推荐方式，在调用 Generate 或 Stream 前
// 直接把 persona 作为输入消息传入。若恢复的历史已经带有同一条系统消息，则不重复添加。
func (a *ToolOpsAgent) withSystemMessage(messages []*schema.Message) []*schema.Message {
	current := a.currentSystemText()
	if first := messages[0]; first != nil && first.Role == schema.System {
		if first.Content == current {
			return messages
		}
		if first.Content == a.systemText || strings.HasPrefix(first.Content, a.systemText+"\n\n当前 Workflow 状态：") {
			result := append([]*schema.Message(nil), messages...)
			result[0] = schema.SystemMessage(current)
			return result
		}
	}
	result := make([]*schema.Message, 0, len(messages)+1)
	result = append(result, schema.SystemMessage(current))
	return append(result, messages...)
}

func (a *ToolOpsAgent) currentSystemText() string {
	if a.workflow == nil {
		return a.systemText
	}
	return fmt.Sprintf("%s\n\n当前 Workflow 状态：%s。只能使用本状态暴露的工具。", a.systemText, a.workflow.Snapshot().State)
}

// ExportGraph 暴露底层 Eino Graph，使单个 ToolOpsAgent 可以作为节点嵌入
// IncidentWorkflow，而不需要把它拆成多个 Agent。
func (a *ToolOpsAgent) ExportGraph() (compose.AnyGraph, []compose.GraphAddNodeOpt) {
	if a == nil || a.runner == nil {
		return nil, nil
	}
	return a.runner.ExportGraph()
}
