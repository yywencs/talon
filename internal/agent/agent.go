package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/config"
	"github.com/wen/opentalon/pkg/prompts"
)

type Agent interface {
	Step(ctx context.Context, state *types.State) (types.Action, error)
}

type llmDecision struct {
	Action          string `json:"action"`
	Message         string `json:"message,omitempty"`
	Command         string `json:"command,omitempty"`
	Result          string `json:"result,omitempty"`
	Thought         string `json:"thought,omitempty"`
	WaitForResponse bool   `json:"wait_for_response,omitempty"`
}

type ThinkingAgent struct {
	client     LLMClient
	promptReg  *prompts.Registry
	promptBld  *PromptBuilder
	model      string
	promptName string
}

func NewThinkingAgent(cfg config.LLMConfig) (*ThinkingAgent, error) {
	client, err := NewLLMClient(cfg)
	if err != nil {
		return nil, err
	}
	return &ThinkingAgent{
		client:     client,
		promptReg:  prompts.NewRegistry(cfg.PromptsDir),
		promptBld:  NewPromptBuilder(),
		model:      cfg.Model,
		promptName: "thinking",
	}, nil
}

func (a *ThinkingAgent) Step(ctx context.Context, state *types.State) (types.Action, error) {
	systemPrompt := a.promptReg.Get("system_prompt").Base()
	userPrompt := a.promptReg.Get("user_prompt").Base()
	return stepWithLLM(ctx, a.client, a.promptBld, a.model, state, systemPrompt, userPrompt)
}

type SearchAgent struct {
	client    LLMClient
	promptReg *prompts.Registry
	promptBld *PromptBuilder
	model     string
}

func NewSearchAgent(cfg config.LLMConfig) (*SearchAgent, error) {
	client, err := NewLLMClient(cfg)
	if err != nil {
		return nil, err
	}
	return &SearchAgent{
		client:    client,
		promptReg: prompts.NewRegistry(cfg.PromptsDir),
		promptBld: NewPromptBuilder(),
		model:     cfg.Model,
	}, nil
}

func (a *SearchAgent) Step(ctx context.Context, state *types.State) (types.Action, error) {
	systemPrompt := a.promptReg.Get("system_prompt").Base()
	userPrompt := a.promptReg.Get("user_prompt").Base()
	return stepWithLLM(ctx, a.client, a.promptBld, a.model, state, systemPrompt, userPrompt)
}

// stepWithLLM 是 agent 推理的核心引擎，完成请求组装、LLM 调用、响应解析三个阶段。
// client、promptBld、model 三个参数由调用方在构造时注入，使函数本身不依赖全局状态。
func stepWithLLM(ctx context.Context, client LLMClient, promptBld *PromptBuilder, model string, state *types.State, systemPrompt string, userPromptExamples string) (types.Action, error) {
	req := ChatRequest{
		Model:       model,
		Messages:    promptBld.BuildMessages(state, systemPrompt, userPromptExamples),
		Schema:      decisionSchema(),
		Temperature: 0,
	}

	resp, err := client.Chat(ctx, req)
	if err != nil {
		return nil, err
	}

	action, err := responseToAction(resp)
	if err != nil {
		return nil, err
	}

	if action == nil {
		return nil, fmt.Errorf("模型输出了无效动作（空 action）")
	}

	action.GetBase().LLMMetrics = &types.Metrics{
		PromptTokens:     resp.PromptTokens,
		CompletionTokens: resp.CompletionTokens,
	}
	return action, nil
}

func decisionSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": []string{"message", "run", "finish"},
			},
			"message": map[string]any{
				"type": "string",
			},
			"command": map[string]any{
				"type": "string",
			},
			"result": map[string]any{
				"type": "string",
			},
			"thought": map[string]any{
				"type": "string",
			},
			"wait_for_response": map[string]any{
				"type": "boolean",
			},
		},
		"required": []string{"action"},
	}
}

func responseToAction(resp *ChatResponse) (types.Action, error) {
	var decision llmDecision
	if err := json.Unmarshal([]byte(resp.Content), &decision); err != nil {
		return nil, fmt.Errorf("解析 LLM 决策失败: %w, 原始输出: %s", err, resp.Content)
	}
	return decisionToAction(decision)
}

func decisionToAction(decision llmDecision) (types.Action, error) {
	switch decision.Action {
	case "message":
		content := strings.TrimSpace(decision.Message)
		if content == "" {
			return nil, nil
		}
		return &types.MessageAction{
			Content:         content,
			WaitForResponse: decision.WaitForResponse,
		}, nil
	case "run":
		command := strings.TrimSpace(decision.Command)
		if command == "" {
			return nil, nil
		}
		return &types.CmdRunAction{
			Command: command,
			Thought: strings.TrimSpace(decision.Thought),
		}, nil
	case "finish":
		return &types.FinishAction{
			Result: strings.TrimSpace(decision.Result),
		}, nil
	default:
		return nil, fmt.Errorf("未知动作类型 %q", decision.Action)
	}
}
