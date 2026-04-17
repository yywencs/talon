package types

import (
	"encoding/json"
	"fmt"
	"strings"
)

func (m Message) MarshalJSON() ([]byte, error) {
	result := map[string]any{
		"role": m.Role,
	}

	if len(m.Content) > 0 {
		blocks := make([]map[string]any, 0, len(m.Content))
		for _, c := range m.Content {
			blocks = append(blocks, c.ToLLMDict()...)
		}
		result["content"] = blocks
	}

	if len(m.ToolCalls) > 0 {
		toolCalls := make([]map[string]any, 0, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			if tc.Origin == OriginResponses {
				toolCalls = append(toolCalls, tc.ToResponsesDict())
			} else {
				toolCalls = append(toolCalls, tc.ToChatDict())
			}
		}
		result["tool_calls"] = toolCalls
	}

	if m.ToolCallID != "" {
		result["tool_call_id"] = m.ToolCallID
	}
	if m.Name != "" {
		result["name"] = m.Name
	}
	if m.ReasoningContent != "" {
		result["reasoning_content"] = m.ReasoningContent
	}
	if len(m.ThinkingBlocks) > 0 {
		blocks := make([]map[string]any, 0, len(m.ThinkingBlocks))
		for _, tb := range m.ThinkingBlocks {
			block := map[string]any{
				"type":     "thinking",
				"thinking": tb.Thinking,
			}
			if tb.Signature != "" {
				block["signature"] = tb.Signature
			}
			blocks = append(blocks, block)
		}
		result["thinking_blocks"] = blocks
	}
	if len(m.RedactedThinkingBlocks) > 0 {
		blocks := make([]map[string]any, 0, len(m.RedactedThinkingBlocks))
		for _, rb := range m.RedactedThinkingBlocks {
			blocks = append(blocks, map[string]any{
				"type": "redacted_thinking",
				"data": rb.Data,
			})
		}
		result["redacted_thinking_blocks"] = blocks
	}
	if m.ResponsesReasoningItem != nil {
		result["responses_reasoning_item"] = m.ResponsesReasoningItem
	}

	return json.Marshal(result)
}

type ResponsesFunctionCallInput struct {
	ID        string `json:"id,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type Origin string

const (
	OriginCompletion Origin = "completion"
	OriginResponses  Origin = "responses"
)

type MessageRole string

const (
	RoleUser      MessageRole = "user"
	RoleSystem    MessageRole = "system"
	RoleAssistant MessageRole = "assistant"
	RoleTool      MessageRole = "tool"
)

type ContentType string

const (
	ContentTypeText  ContentType = "text"
	ContentTypeImage ContentType = "image"
)

type Content interface {
	Type() ContentType
	ToLLMDict() []map[string]any
}

type BaseContent struct {
	CachePrompt bool `json:"cache_prompt,omitempty"`
}

type TextContent struct {
	BaseContent
	Text string `json:"text"`
}

func (c TextContent) Type() ContentType { return ContentTypeText }

func (c TextContent) ToLLMDict() []map[string]any {
	block := map[string]any{"type": "text", "text": c.Text}
	if c.CachePrompt {
		block["cache_control"] = map[string]string{"type": "ephemeral"}
	}
	return []map[string]any{block}
}

type ImageContent struct {
	BaseContent
	ImageURLs []string `json:"image_urls"`
}

func (c ImageContent) Type() ContentType { return ContentTypeImage }

func (c ImageContent) ToLLMDict() []map[string]any {
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
}

type MessageToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Origin    Origin `json:"origin"`
}

type ChatToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ChatToolCallInput struct {
	ID       string                `json:"id"`
	Type     string                `json:"type"`
	Function *ChatToolCallFunction `json:"function"`
}

func MessageToolCallFromChatToolCall(toolCall ChatToolCallInput) (*MessageToolCall, error) {
	if toolCall.Type != "function" {
		return nil, fmt.Errorf("unsupported tool call type, expected function, got %q", toolCall.Type)
	}
	if toolCall.Function == nil {
		return nil, fmt.Errorf("tool_call.function is nil")
	}
	if toolCall.Function.Name == "" {
		return nil, fmt.Errorf("tool_call.function.name is empty")
	}
	return &MessageToolCall{
		ID:        toolCall.ID,
		Name:      toolCall.Function.Name,
		Arguments: toolCall.Function.Arguments,
		Origin:    OriginCompletion,
	}, nil
}

func MessageToolCallFromResponsesFunctionCall(item ResponsesFunctionCallInput) (*MessageToolCall, error) {
	callID := item.CallID
	if callID == "" {
		callID = item.ID
	}
	if callID == "" {
		return nil, fmt.Errorf("responses function_call missing call_id/id")
	}
	if item.Name == "" {
		return nil, fmt.Errorf("responses function_call missing name")
	}
	return &MessageToolCall{
		ID:        callID,
		Name:      item.Name,
		Arguments: item.Arguments,
		Origin:    OriginResponses,
	}, nil
}

func (m MessageToolCall) ToChatDict() map[string]any {
	return map[string]any{
		"id":   m.ID,
		"type": "function",
		"function": map[string]any{
			"name":      m.Name,
			"arguments": m.Arguments,
		},
	}
}

func (m MessageToolCall) ToResponsesDict() map[string]any {
	respID := m.ID
	if !strings.HasPrefix(respID, "fc") {
		respID = "fc_" + respID
	}
	args := m.Arguments
	if !json.Valid([]byte(args)) {
		raw, _ := json.Marshal(args)
		args = string(raw)
	}
	return map[string]any{
		"type":      "function_call",
		"id":        respID,
		"call_id":   respID,
		"name":      m.Name,
		"arguments": args,
	}
}

type ThinkingBlock struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature,omitempty"`
}

type RedactedThinkingBlock struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

type ReasoningItemModel struct {
	ID               string   `json:"id,omitempty"`
	Summary          []string `json:"summary,omitempty"`
	Content          []string `json:"content,omitempty"`
	EncryptedContent string   `json:"encrypted_content,omitempty"`
	Status           string   `json:"status,omitempty"`
}

type Message struct {
	Role                   MessageRole             `json:"role"`
	Content                []Content               `json:"content,omitempty"`
	ToolCalls              []MessageToolCall       `json:"tool_calls,omitempty"`
	ToolCallID             string                  `json:"tool_call_id,omitempty"`
	Name                   string                  `json:"name,omitempty"`
	ReasoningContent       string                  `json:"reasoning_content,omitempty"`
	ThinkingBlocks         []ThinkingBlock         `json:"thinking_blocks,omitempty"`
	RedactedThinkingBlocks []RedactedThinkingBlock `json:"redacted_thinking_blocks,omitempty"`
	ResponsesReasoningItem *ReasoningItemModel     `json:"responses_reasoning_item,omitempty"`
}

func FlattenTextContent(contents []Content) string {
	var parts []string
	for _, item := range contents {
		switch c := item.(type) {
		case TextContent:
			if strings.TrimSpace(c.Text) != "" {
				parts = append(parts, c.Text)
			}
		case *TextContent:
			if c != nil && strings.TrimSpace(c.Text) != "" {
				parts = append(parts, c.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}
