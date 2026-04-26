package agent

import (
	"github.com/wen/opentalon/internal/serializer"
	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/utils"
)

// convertToolsToAny 将 []map[string]any 转换为 []any。
func convertToolsToAny(tools []map[string]any) []any {
	result := make([]any, len(tools))
	for i, tool := range tools {
		result[i] = tool
	}
	return result
}

func toOllamaMessages(messages []types.Message) []any {
	out := make([]any, 0, len(messages))
	for _, msg := range messages {
		content := utils.FlattenTextContent(msg.Content)
		if len(msg.ToolCalls) > 0 {
			content = ""
		}
		m := map[string]any{
			"role":    string(msg.Role),
			"content": content,
		}
		out = append(out, m)
	}
	return out
}

func serializeOpenAIChatMessages(messages []types.Message) ([]any, error) {
	messageSerializer := &serializer.OpenAIChatSerializer{
		CacheEnabled:           true,
		VisionEnabled:          true,
		FunctionCallingEnabled: true,
		SendReasoningContent:   true,
	}
	return serializer.SerializeMessages(messageSerializer, messages)
}

func buildAssistantMessage(role string, content string, toolCalls []types.MessageToolCall, reasoningContent string) types.Message {
	msg := types.Message{
		Role:             types.MessageRole(role),
		ToolCalls:        toolCalls,
		ReasoningContent: reasoningContent,
	}
	if msg.Role == "" {
		msg.Role = types.RoleAssistant
	}
	if content != "" {
		msg.Content = []types.Content{
			types.TextContent{Text: content},
		}
	}
	return msg
}

func messageFromOpenAIChoice(wireMsg openAIWireMessage) (types.Message, error) {
	toolCalls := make([]types.MessageToolCall, 0, len(wireMsg.ToolCalls))
	for _, toolCall := range wireMsg.ToolCalls {
		converted, err := types.MessageToolCallFromChatToolCall(toolCall)
		if err != nil {
			return types.Message{}, err
		}
		toolCalls = append(toolCalls, *converted)
	}
	return buildAssistantMessage(wireMsg.Role, wireMsg.Content, toolCalls, wireMsg.ReasoningContent), nil
}

func flattenStreamToolCalls(streamToolCalls map[int]*types.MessageToolCall) []types.MessageToolCall {
	if len(streamToolCalls) == 0 {
		return nil
	}
	maxIndex := -1
	for idx := range streamToolCalls {
		if idx > maxIndex {
			maxIndex = idx
		}
	}
	toolCalls := make([]types.MessageToolCall, 0, maxIndex+1)
	for idx := 0; idx <= maxIndex; idx++ {
		toolCall := streamToolCalls[idx]
		if toolCall == nil {
			continue
		}
		toolCalls = append(toolCalls, *toolCall)
	}
	return toolCalls
}

func stripCacheControl(messages []types.Message) []types.Message {
	if len(messages) == 0 {
		return nil
	}
	sanitized := make([]types.Message, len(messages))
	copy(sanitized, messages)
	for i := range sanitized {
		if len(sanitized[i].Content) == 0 {
			continue
		}
		sanitized[i].Content = cloneContentWithoutCache(sanitized[i].Content)
	}
	return sanitized
}

func cloneContentWithoutCache(contents []types.Content) []types.Content {
	cloned := make([]types.Content, 0, len(contents))
	for _, content := range contents {
		switch c := content.(type) {
		case types.TextContent:
			c.CachePrompt = false
			cloned = append(cloned, c)
		case *types.TextContent:
			if c == nil {
				continue
			}
			dup := *c
			dup.CachePrompt = false
			cloned = append(cloned, dup)
		case types.ImageContent:
			c.CachePrompt = false
			cloned = append(cloned, c)
		case *types.ImageContent:
			if c == nil {
				continue
			}
			dup := *c
			dup.CachePrompt = false
			cloned = append(cloned, dup)
		default:
			cloned = append(cloned, content)
		}
	}
	return cloned
}
