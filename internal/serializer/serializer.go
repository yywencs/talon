// serializer/serializer.go
package serializer

import (
	"encoding/json"
	"strings"

	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/logger"
	"github.com/wen/opentalon/pkg/utils"
)

// Serializer 统一序列化接口
type Serializer interface {
	Serialize(msg types.Message) (any, error)
}

// SerializeMessages 将消息列表序列化为 wire 协议可直接编码的结构。
func SerializeMessages(s Serializer, messages []types.Message) ([]any, error) {
	result := make([]any, 0, len(messages))
	for _, msg := range messages {
		item, err := s.Serialize(msg)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

// ------------------------------ OpenAI Chat 序列化器 ------------------------------
type OpenAIChatSerializer struct {
	CacheEnabled           bool
	VisionEnabled          bool
	FunctionCallingEnabled bool
	ForceStringSerializer  bool
	SendReasoningContent   bool
}

func (s *OpenAIChatSerializer) Serialize(msg types.Message) (any, error) {
	if !s.ForceStringSerializer && (s.CacheEnabled || s.VisionEnabled || s.FunctionCallingEnabled) {
		return s.serializeList(msg)
	}
	return s.serializeString(msg)
}

func (s *OpenAIChatSerializer) serializeString(msg types.Message) (map[string]any, error) {
	content := utils.FlattenTextContent(msg.Content)
	if msg.Role == types.RoleTool {
		content = utils.MaybeTruncateToolText(content)
	}
	result := map[string]any{
		"role":    msg.Role,
		"content": content,
	}
	s.injectToolCalls(result, msg)
	s.injectToolResult(result, msg)
	if s.SendReasoningContent && msg.ReasoningContent != "" {
		result["reasoning_content"] = msg.ReasoningContent
	}
	return result, nil
}

func (s *OpenAIChatSerializer) serializeList(msg types.Message) (map[string]any, error) {
	content := make([]map[string]any, 0)
	roleToolWithPromptCaching := false

	// 处理思考块
	thinkingBlocks := s.serializeThinkingBlocks(msg)

	// 处理 Content
	for _, c := range msg.Content {
		itemDicts := serializeContentBlocks(c)

		// 工具内容特殊处理
		if msg.Role == types.RoleTool {
			itemDicts = s.processToolContent(itemDicts)
			if hasCachePrompt(c) {
				roleToolWithPromptCaching = true
				for i := range itemDicts {
					delete(itemDicts[i], "cache_control")
				}
			}
		}

		// 图像内容过滤
		if _, ok := c.(types.ImageContent); ok && s.VisionEnabled {
			content = append(content, itemDicts...)
		} else if _, ok := c.(types.TextContent); ok {
			content = append(content, itemDicts...)
		}
	}

	result := map[string]any{
		"role":    msg.Role,
		"content": content,
	}
	if roleToolWithPromptCaching {
		result["cache_control"] = map[string]string{"type": "ephemeral"}
	}
	if len(thinkingBlocks) > 0 {
		result["thinking_blocks"] = thinkingBlocks
	}

	s.injectToolCalls(result, msg)
	s.injectToolResult(result, msg)
	s.removeEmptyContent(result)

	return result, nil
}

func (s *OpenAIChatSerializer) serializeThinkingBlocks(msg types.Message) []map[string]any {
	if msg.Role != types.RoleAssistant {
		return nil
	}
	blocks := make([]map[string]any, 0, len(msg.ThinkingBlocks))
	for _, tb := range msg.ThinkingBlocks {
		block := map[string]any{
			"type":     "thinking",
			"thinking": tb.Thinking,
		}
		if tb.Signature != "" {
			block["signature"] = tb.Signature
		}
		blocks = append(blocks, block)
	}
	return blocks
}

func (s *OpenAIChatSerializer) processToolContent(itemDicts []map[string]any) []map[string]any {
	processed := make([]map[string]any, 0, len(itemDicts))
	for _, d := range itemDicts {
		if d["type"] == "text" {
			if text, ok := d["text"].(string); ok {
				d["text"] = utils.MaybeTruncateToolText(text)
			}
		}
		processed = append(processed, d)
	}
	return processed
}

func (s *OpenAIChatSerializer) injectToolCalls(result map[string]any, msg types.Message) {
	if msg.Role == types.RoleAssistant && len(msg.ToolCalls) > 0 {
		toolCalls := make([]map[string]any, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			if tc.Origin == types.OriginResponses {
				responsesDict, err := serializeResponsesToolCall(tc)
				if err != nil {
					logger.Error("序列化响应工具调用失败:",
						"err", err)
					return
				}
				toolCalls = append(toolCalls, responsesDict)

			} else {
				toolCalls = append(toolCalls, serializeChatToolCall(tc))
			}
		}
		result["tool_calls"] = toolCalls
	}
}

func (s *OpenAIChatSerializer) injectToolResult(result map[string]any, msg types.Message) {
	if msg.Role == types.RoleTool && msg.ToolCallID != "" {
		if msg.Name == "" {
			return // 可以返回错误，或者忽略
		}
		result["tool_call_id"] = msg.ToolCallID
		result["name"] = msg.Name
	}
}

func (s *OpenAIChatSerializer) removeEmptyContent(result map[string]any) {
	content, ok := result["content"]
	if !ok {
		return
	}

	switch v := content.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			delete(result, "content")
		}
	case []map[string]any:
		normalized := make([]map[string]any, 0)
		for _, item := range v {
			if item["type"] == "text" {
				if text, ok := item["text"].(string); ok && strings.TrimSpace(text) == "" {
					continue
				}
			}
			normalized = append(normalized, item)
		}
		if len(normalized) > 0 {
			result["content"] = normalized
		} else {
			delete(result, "content")
		}
	}
}

func serializeContentBlocks(content types.Content) []map[string]any {
	switch c := content.(type) {
	case types.TextContent:
		block := map[string]any{
			"type": "text",
			"text": c.Text,
		}
		if c.CachePrompt {
			block["cache_control"] = map[string]string{"type": "ephemeral"}
		}
		return []map[string]any{block}
	case types.ImageContent:
		items := make([]map[string]any, 0, len(c.ImageURLs))
		for idx, url := range c.ImageURLs {
			item := map[string]any{
				"type":      "image_url",
				"image_url": map[string]string{"url": url},
			}
			if c.CachePrompt && idx == len(c.ImageURLs)-1 {
				item["cache_control"] = map[string]string{"type": "ephemeral"}
			}
			items = append(items, item)
		}
		return items
	default:
		return nil
	}
}

func hasCachePrompt(content types.Content) bool {
	switch c := content.(type) {
	case types.TextContent:
		return c.CachePrompt
	case types.ImageContent:
		return c.CachePrompt
	default:
		return false
	}
}

func serializeChatToolCall(call types.MessageToolCall) map[string]any {
	return map[string]any{
		"id":   call.ID,
		"type": "function",
		"function": map[string]any{
			"name":      call.Name,
			"arguments": call.Arguments,
		},
	}
}

func serializeResponsesToolCall(call types.MessageToolCall) (map[string]any, error) {
	respID := call.ID
	if !strings.HasPrefix(respID, "fc") {
		respID = "fc_" + respID
	}

	args := call.Arguments
	if !json.Valid([]byte(args)) {
		raw, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		args = string(raw)
	}

	return map[string]any{
		"type":      "function_call",
		"id":        respID,
		"call_id":   respID,
		"name":      call.Name,
		"arguments": args,
	}, nil
}

// ------------------------------ OpenAI Responses 序列化器 ------------------------------
type OpenAIResponsesSerializer struct {
	VisionEnabled bool
}

func (s *OpenAIResponsesSerializer) Serialize(msg types.Message) (any, error) {
	// 实现原有的 to_responses_dict 逻辑
	// ... 省略具体实现，结构和 OpenAIChatSerializer 类似
	return nil, nil
}

// ------------------------------ Anthropic 序列化器 ------------------------------
type AnthropicSerializer struct {
	// 配置项
}

func (s *AnthropicSerializer) Serialize(msg types.Message) (any, error) {
	// 实现 Anthropic 特定的序列化逻辑
	// ... 省略具体实现
	return nil, nil
}
