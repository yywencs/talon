package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/config"
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

const baseAgentInstructions = `你是一个命令行编码智能体。你必须只输出符合 schema 的 JSON，对应下一步唯一动作。

可选动作:
1. action=message: 给用户发消息。若需要等待用户继续输入，设置 wait_for_response=true。**message 字段禁止为空，空 message 视为无效动作。**
2. action=run: 执行一条 shell 命令。command 必填。命令尽量短小、安全、可验证。
3. action=finish: 任务完成。可在 result 中给出最终结果摘要。

决策规则:
- 如果需要获取环境信息、查看文件、运行命令，优先使用 run。
- 如果用户请求已经被命令结果完整满足，**必须返回 finish，不能返回 message**。
- 如果需要向用户报告中间进展或询问确认，使用 message，且 message 字段必须有实质内容。
- **绝对不要输出空 message**，空 message 会被系统视为错误并导致任务失败。
- thought 字段可以简短说明原因。`

type ThinkingAgent struct {
	client    llmClient
	promptBld *PromptBuilder
	model     string
}

func NewThinkingAgent(cfg config.LLMConfig) (*ThinkingAgent, error) {
	client, err := newLLMClient(cfg)
	if err != nil {
		return nil, err
	}
	return &ThinkingAgent{
		client:    client,
		promptBld: NewPromptBuilder(),
		model:     cfg.Model,
	}, nil
}

func (a *ThinkingAgent) Step(ctx context.Context, state *types.State) (types.Action, error) {
	return stepWithLLM(ctx, a.client, a.promptBld, a.model, state, baseAgentInstructions+"\n\n当前模式: 通用推理与编码助手。")
}

type SearchAgent struct{}

func NewSearchAgent() *SearchAgent {
	return &SearchAgent{}
}

func (a *SearchAgent) Step(ctx context.Context, state *types.State) (types.Action, error) {
	cfg := ensureConfig()
	client, err := newLLMClient(cfg.LLM)
	if err != nil {
		return nil, err
	}
	return stepWithLLM(ctx, client, NewPromptBuilder(), cfg.LLM.Model, state, baseAgentInstructions+"\n\n当前模式: 搜索优先。你应更积极地使用 run 执行 grep、find、ls 等检索命令。")
}

// stepWithLLM 是 agent 推理的核心引擎，完成请求组装、LLM 调用、响应解析三个阶段。
// client、promptBld、model 三个参数由调用方在构造时注入，使函数本身不依赖全局状态。
func stepWithLLM(ctx context.Context, client llmClient, promptBld *PromptBuilder, model string, state *types.State, systemPrompt string) (types.Action, error) {
	req := llmChatRequest{
		Model:       model,
		Messages:    promptBld.BuildMessages(state, systemPrompt),
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

func ensureConfig() *config.Config {
	if config.Global == nil {
		config.Load()
	}
	return config.Global
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

func responseToAction(resp *llmChatResponse) (types.Action, error) {
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
