package agent

import (
	"context"
	"fmt"

	toolpkg "github.com/wen/opentalon/internal/tool"
	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/config"
	"github.com/wen/opentalon/pkg/prompts"
	"github.com/wen/opentalon/pkg/utils"
)

type Agent struct {
	client    LLMClient
	promptReg *prompts.Registry
	promptBld *PromptBuilder
	model     string
	tools     []map[string]any // 缓存的工具schema
}

// toolCallBatch 表示一次模型响应解析出的工具调用批次。
type toolCallBatch struct {
	calls    []types.MessageToolCall
	finished bool
}

// truncateAtFinish 在工具调用批次中查找 finish 工具调用，并截断后续调用。
func (b *toolCallBatch) truncateAtFinish() {
	if b == nil || len(b.calls) == 0 {
		return
	}
	b.finished = false
	for idx, call := range b.calls {
		if call.Name == string(types.ActionFinish) {
			b.calls = b.calls[:idx+1]
			b.finished = true
			return
		}
	}
}

func NewAgent(cfg config.LLMConfig) (*Agent, error) {
	client, err := NewLLMClient(cfg)
	if err != nil {
		return nil, err
	}

	// 缓存所有工具的schema用于function calling
	tools, err := toolpkg.ToolsToOpenAITools(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to build tools schema: %w", err)
	}

	return &Agent{
		client:    client,
		promptReg: prompts.NewRegistry(cfg.PromptsDir),
		promptBld: NewPromptBuilder(),
		model:     cfg.Model,
		tools:     tools,
	}, nil
}

// StreamStep 执行一次完整的 LLM 循环，并通过回调输出增量语义结果。
func (a *Agent) StreamStep(ctx context.Context, state *types.SessionState, onOutput func(types.AgentOutput)) (*types.AgentTurnResult, error) {
	systemPrompt := a.promptReg.Get("system_prompt").Base()
	userPrompt := a.promptReg.Get("user_prompt").Base()

	req := ChatRequest{
		Model:    a.model,
		Messages: a.promptBld.BuildMessages(state, systemPrompt, userPrompt),
		Tools:    a.tools,
		Stream:   true,
	}

	resp, err := a.client.StreamChat(ctx, req, func(token string) {
		if onOutput == nil || token == "" {
			return
		}
		onOutput(types.AgentOutput{
			Kind:      types.AgentOutputMessageDelta,
			TextDelta: token,
		})
	})
	if err != nil {
		return nil, err
	}

	result, err := a.responseToTurnResult(resp)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("模型未输出有效结果")
	}
	if result.Message == nil && len(result.ToolCalls) == 0 {
		return nil, fmt.Errorf("模型未输出有效结果")
	}
	return result, nil
}

// responseToTurnResult 将模型响应转换为语义结果。
func (a *Agent) responseToTurnResult(resp *ChatResponse) (*types.AgentTurnResult, error) {
	if resp == nil {
		return nil, fmt.Errorf("responseToTurnResult: response is nil")
	}

	toolCalls, plainText, err := a.ParseLLMResponse(resp)
	message := buildAssistantOutputMessage(resp.Message, plainText)
	actionReasoningContent := ""
	if len(toolCalls) > 0 {
		actionReasoningContent = resp.Message.ReasoningContent
		message = stripActionReasoningMessage(message)
	}

	if err != nil && plainText == "" {
		return nil, fmt.Errorf("responseToTurnResult: parse llm response failed: %w", err)
	}

	batch := &toolCallBatch{calls: toolCalls}
	batch.truncateAtFinish()
	return &types.AgentTurnResult{
		Message:                message,
		ToolCalls:              batch.calls,
		ActionReasoningContent: actionReasoningContent,
		Finished:               batch.finished || (message != nil && len(batch.calls) == 0),
	}, nil
}

func (r *Agent) ParseLLMResponse(resp *ChatResponse) ([]types.MessageToolCall, string, error) {
	if resp == nil {
		return nil, "", fmt.Errorf("nil LLM response")
	}

	plainText := utils.FlattenTextContent(resp.Message.Content)
	if len(resp.Message.ToolCalls) == 0 {
		return nil, plainText, nil
	}

	calls := make([]types.MessageToolCall, 0, len(resp.Message.ToolCalls))
	for _, tc := range resp.Message.ToolCalls {
		calls = append(calls, types.MessageToolCall{
			ID:        tc.ID,
			Name:      tc.Name,
			Arguments: tc.Arguments,
		})
	}
	return calls, plainText, nil
}

func buildAssistantOutputMessage(msg types.Message, plainText string) *types.Message {
	if len(msg.ToolCalls) > 0 {
		msg.ToolCalls = nil
	}
	if plainText != "" {
		msg.Content = []types.Content{
			types.TextContent{Text: plainText},
		}
	}
	if !hasAssistantMessagePayload(msg) {
		return nil
	}
	cloned := msg
	return &cloned
}

func stripActionReasoningMessage(msg *types.Message) *types.Message {
	if msg == nil {
		return nil
	}
	cloned := *msg
	cloned.ReasoningContent = ""
	if !hasAssistantMessagePayload(cloned) {
		return nil
	}
	return &cloned
}

func hasAssistantMessagePayload(msg types.Message) bool {
	return len(msg.Content) > 0 ||
		msg.ReasoningContent != "" ||
		len(msg.ThinkingBlocks) > 0 ||
		len(msg.RedactedThinkingBlocks) > 0 ||
		msg.ResponsesReasoningItem != nil
}
