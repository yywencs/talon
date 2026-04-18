package types

import "fmt"

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
}

type BaseContent struct {
	CachePrompt bool `json:"cache_prompt,omitempty"`
}

type TextContent struct {
	BaseContent
	Text string `json:"text"`
}

func (c TextContent) Type() ContentType { return ContentTypeText }

type ImageContent struct {
	BaseContent
	ImageURLs []string `json:"image_urls"`
}

func (c ImageContent) Type() ContentType { return ContentTypeImage }

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
