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
	"github.com/wen/opentalon/internal/platform"
	toolset "github.com/wen/opentalon/internal/tools"
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

	// AdditionalInstructions 只能补充当前部署环境的调查说明，
	// 固定的安全边界始终由 Agent 内置提示词和 Platform 共同保证。
	AdditionalInstructions string
}

// ToolOpsAgent 使用 Eino ReAct 循环调查 Incident，并调用受控的 ToolOps 工具。
// 每个实例只绑定一个 Incident，避免模型跨事件查询或执行动作。
type ToolOpsAgent struct {
	incidentID string
	systemText string
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

	tools, err := toolset.New(ctx, config.Platform, incidentID)
	if err != nil {
		return nil, fmt.Errorf("build incident tools: %w", err)
	}

	persona := systemPrompt(incidentID, config.AdditionalInstructions)
	runner, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: config.Model,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools:               tools.Tools(),
			ExecuteSequentially: true,
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

	return &ToolOpsAgent{incidentID: incidentID, systemText: persona, runner: runner}, nil
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
		schema.SystemMessage(a.systemText),
		schema.UserMessage(instruction),
	}
	return a.runner.Generate(ctx, messages, opts...)
}

// Generate 使用已有消息继续一次 Agent 调用，供后续工作流恢复上下文时使用。
func (a *ToolOpsAgent) Generate(ctx context.Context, messages []*schema.Message, opts ...flowagent.AgentOption) (*schema.Message, error) {
	if a == nil || a.runner == nil {
		return nil, fmt.Errorf("ToolOpsAgent is not initialized")
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("agent messages are required")
	}
	return a.runner.Generate(ctx, a.withSystemMessage(messages), opts...)
}

// Stream 以流式方式运行 Agent，主要用于命令行或管理界面展示。
func (a *ToolOpsAgent) Stream(ctx context.Context, messages []*schema.Message, opts ...flowagent.AgentOption) (*schema.StreamReader[*schema.Message], error) {
	if a == nil || a.runner == nil {
		return nil, fmt.Errorf("ToolOpsAgent is not initialized")
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("agent messages are required")
	}
	return a.runner.Stream(ctx, a.withSystemMessage(messages), opts...)
}

// withSystemMessage 按照 Eino ReAct 的推荐方式，在调用 Generate 或 Stream 前
// 直接把 persona 作为输入消息传入。若恢复的历史已经带有同一条系统消息，则不重复添加。
func (a *ToolOpsAgent) withSystemMessage(messages []*schema.Message) []*schema.Message {
	if first := messages[0]; first != nil && first.Role == schema.System && first.Content == a.systemText {
		return messages
	}
	result := make([]*schema.Message, 0, len(messages)+1)
	result = append(result, schema.SystemMessage(a.systemText))
	return append(result, messages...)
}

// ExportGraph 暴露底层 Eino Graph，使单个 ToolOpsAgent 可以作为节点嵌入
// IncidentWorkflow，而不需要把它拆成多个 Agent。
func (a *ToolOpsAgent) ExportGraph() (compose.AnyGraph, []compose.GraphAddNodeOpt) {
	if a == nil || a.runner == nil {
		return nil, nil
	}
	return a.runner.ExportGraph()
}
