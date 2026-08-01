package types

import "fmt"

// ResponsesFunctionCallInput 是 OpenAI Responses API（/v1/responses）中
// function_call 类型条目的 wire 协议结构。与 chat/completions 的 tool_call
// 字段命名不同（call_id vs id），因此需要独立的转换函数。
type ResponsesFunctionCallInput struct {
	ID        string `json:"id,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// Origin 标记一个工具调用来自哪种 API 协议，影响序列化和回放时的字段映射。
type Origin string

const (
	// OriginCompletion 表示来自 chat/completions 的 tool_call。
	OriginCompletion Origin = "completion"
	// OriginResponses 表示来自 Responses API 的 function_call。
	OriginResponses Origin = "responses"
)

// MessageRole 对应 LLM 对话协议中的角色枚举。
type MessageRole string

const (
	RoleUser      MessageRole = "user"
	RoleSystem    MessageRole = "system"
	RoleAssistant MessageRole = "assistant"
	RoleTool      MessageRole = "tool"
)

// ContentType 区分多模态内容的类型，用于 Content 接口的类型分发。
type ContentType string

const (
	ContentTypeText  ContentType = "text"
	ContentTypeImage ContentType = "image"
)

// Content 是消息内容块的多态接口。一条 Message 可携带多个 Content，
// 支持 text 与 image 混合的多模态输入。
type Content interface {
	Type() ContentType
}

// BaseContent 是所有 Content 实现共享的公共字段。
// CachePrompt 控制是否向 provider 标记该内容块为 prompt cache 前缀，
// 用于减少重复大上下文的计费和延迟。
type BaseContent struct {
	CachePrompt bool `json:"cache_prompt,omitempty"`
}

// TextContent 是纯文本内容块。
type TextContent struct {
	BaseContent
	Text string `json:"text"`
}

func (c TextContent) Type() ContentType { return ContentTypeText }

// ImageContent 是图片内容块，通过 URL 引用外部图片。
type ImageContent struct {
	BaseContent
	ImageURLs []string `json:"image_urls"`
}

func (c ImageContent) Type() ContentType { return ContentTypeImage }

// MessageToolCall 是统一后的工具调用表示，屏蔽 provider 协议差异。
// ID 用于将后续的 Observation 关联回这条调用（因果匹配）。
type MessageToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Origin    Origin `json:"origin"`
}

// ChatToolCallFunction 是 chat/completions 协议中 tool_calls[].function 子结构。
type ChatToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatToolCallInput 是 chat/completions 协议中工具调用的条目结构，
// 经 MessageToolCallFromChatToolCall 转换为统一的 MessageToolCall。
type ChatToolCallInput struct {
	ID       string                `json:"id"`
	Type     string                `json:"type"`
	Function *ChatToolCallFunction `json:"function"`
}

// MessageToolCallFromChatToolCall 将 chat/completions 的工具调用条目
// 转换为统一的 MessageToolCall。仅接受 type=function 的条目。
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

// MessageToolCallFromResponsesFunctionCall 将 Responses API 的 function_call 条目
// 转换为统一的 MessageToolCall。CallID 优先于 ID 作为关联键。
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

// ThinkingBlock 表示 Claude 的思维链（extended thinking）中的一个可见块，
// 包含原始思维文本和服务端签发的加密签名用于后续验签。
type ThinkingBlock struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature,omitempty"`
}

// RedactedThinkingBlock 表示被服务端脱敏的思维块，
// 明文不可见，仅保留脱敏后的加密数据。
type RedactedThinkingBlock struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

// ReasoningItemModel 是 Responses API 中 reasoning 条目的结构化表示，
// carries summary / content / encrypted_content 等推理过程元数据。
type ReasoningItemModel struct {
	ID               string   `json:"id,omitempty"`
	Summary          []string `json:"summary,omitempty"`
	Content          []string `json:"content,omitempty"`
	EncryptedContent string   `json:"encrypted_content,omitempty"`
	Status           string   `json:"status,omitempty"`
}

// Message 是贯穿整个系统使用的统一对话消息结构，屏蔽不同 provider 的 wire 协议差异。
// 携带多模态内容、工具调用、思维链及 Responses API 推理元数据。
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
