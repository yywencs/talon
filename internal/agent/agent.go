package agent

import (
	"context"
	"fmt"

	toolpkg "github.com/wen/opentalon/internal/tool"
	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/config"
	"github.com/wen/opentalon/pkg/prompts"
)

type Agent interface {
	Step(ctx context.Context, state *types.SessionState) (types.Action, error)
}

type BaseAgent struct {
	client    LLMClient
	promptReg *prompts.Registry
	promptBld *PromptBuilder
	model     string
	router    *ToolRouter
	tools     []map[string]any // 缓存的工具schema
}

func NewBaseAgent(cfg config.LLMConfig) (*BaseAgent, error) {
	client, err := NewLLMClient(cfg)
	if err != nil {
		return nil, err
	}

	router := NewToolRouter()

	// 缓存所有工具的schema用于function calling
	tools, err := toolpkg.ToolsToOpenAITools(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to build tools schema: %w", err)
	}

	return &BaseAgent{
		client:    client,
		promptReg: prompts.NewRegistry(cfg.PromptsDir),
		promptBld: NewPromptBuilder(),
		model:     cfg.Model,
		router:    router,
		tools:     tools,
	}, nil
}

func (a *BaseAgent) Step(ctx context.Context, state *types.SessionState) (types.Action, error) {
	systemPrompt := a.promptReg.Get("system_prompt").Base()
	userPrompt := a.promptReg.Get("user_prompt").Base()
	return stepWithLLM(ctx, a.client, a.promptBld, a.model, state, systemPrompt, userPrompt, a.router, a.tools)
}

func stepWithLLM(ctx context.Context, client LLMClient, promptBld *PromptBuilder, model string, state *types.SessionState, systemPrompt string, userPromptExamples string, router *ToolRouter, tools []map[string]any) (types.Action, error) {
	req := ChatRequest{
		Model:    model,
		Messages: promptBld.BuildMessages(state, systemPrompt, userPromptExamples),
		Tools:    tools, // 新增：传递工具schema
	}

	resp, err := client.Chat(ctx, req)
	if err != nil {
		return nil, err
	}

	action, err := responseToAction(resp, router)
	if err != nil {
		return nil, err
	}
	if action == nil {
		return nil, fmt.Errorf("模型未输出有效动作")
	}
	return action, nil
}

func responseToAction(resp *ChatResponse, router *ToolRouter) (types.Action, error) {
	if resp == nil {
		return nil, nil
	}

	toolCalls, plainText, err := router.ParseLLMResponse(resp)
	if err != nil || len(toolCalls) > 0 {
		if len(toolCalls) > 0 {
			return &ToolCallAction{
				ToolCalls: toolCalls,
				PlainText: plainText,
				Router:    router,
			}, nil
		}
	}

	if err != nil && plainText == "" {
		return &types.MessageAction{
			Content:         "解析工具调用失败: " + err.Error(),
			WaitForResponse: false,
		}, nil
	}

	if plainText != "" {
		return &types.MessageAction{
			Content:         plainText,
			WaitForResponse: true,
		}, nil
	}

	return nil, nil
}
