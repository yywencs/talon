// Package types 定义了 Agent 系统内部使用的核心数据类型。
// 这些类型描述了系统中流动的所有事件（Event）、动作（Action）和观察结果（Observation）。
//
// 类型设计原则：
//   - 所有事件共享 BaseEvent，提供统一的 ID、Timestamp、Source 等元字段。
//   - Action 和 Observation 是两个互不重叠的接口，Controller 通过类型 switch 分发处理。
//   - PendingAction 机制要求每个 Action 的 ID 在整个会话内唯一，用于因果匹配。
package types

// SecurityRisk 标注动作的安全敏感等级，用于审批策略和终端着色展示。
type SecurityRisk string

const (
	SecurityRisk_UNKNOWN SecurityRisk = "UNKNOWN"
	SecurityRisk_LOW     SecurityRisk = "LOW"
	SecurityRisk_MEDIUM  SecurityRisk = "MEDIUM"
	SecurityRisk_HIGH    SecurityRisk = "HIGH"
)

// weight 将等级映射为可比较的整数权重，UNKNOWN 固定返回 0。
func (s SecurityRisk) weight() int {
	switch s {
	case SecurityRisk_LOW:
		return 1
	case SecurityRisk_MEDIUM:
		return 2
	case SecurityRisk_HIGH:
		return 3
	default:
		return 0 // UNKNOWN 或其他非法值
	}
}

// IsRiskierOrEqual 判断当前等级是否不低于 other；任一方为 UNKNOWN 时返回 false。
func (s SecurityRisk) IsRiskierOrEqual(other SecurityRisk) bool {
	if s == SecurityRisk_UNKNOWN || other == SecurityRisk_UNKNOWN {
		return false
	}
	return s.weight() >= other.weight()
}

// Color 返回该等级对应的终端 ANSI 颜色前缀，用于 CLI 展示。
func (s SecurityRisk) Color() string {
	switch s {
	case SecurityRisk_LOW:
		return "\033[32m" // Green
	case SecurityRisk_MEDIUM:
		return "\033[33m" // Yellow
	case SecurityRisk_HIGH:
		return "\033[31m" // Red
	default:
		return "\033[37m" // White
	}
}

// ToolMetadata 是每个动作附带的元信息，供展示和审批使用。
type ToolMetadata struct {
	Summary      string       `json:"summary" jsonschema:"description=动作摘要"`
	SecurityRisk SecurityRisk `json:"security_risk" jsonschema:"description=风险等级"`
}

// ActionType 定义动作的类型，用于区分不同类别的动作。
type ActionType string

const (
	ActionRun     ActionType = "run"
	ActionRead    ActionType = "read"
	ActionWrite   ActionType = "write"
	ActionEdit    ActionType = "edit"
	ActionMessage ActionType = "message"
	ActionFinish  ActionType = "finish"
)

// ActionEvent 是 Action 的事件信封，用于将业务动作与系统元信息解耦。
type ActionEvent struct {
	BaseEvent
	ActionID   string     `json:"action_id"`
	ActionType ActionType `json:"action_type"`
	ToolName   string     `json:"tool_name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`

	LLMResponseID string       `json:"llm_response_id,omitempty"`
	Summary       string       `json:"summary,omitempty"`
	SecurityRisk  SecurityRisk `json:"security_risk,omitempty"`

	Content                TextContent             `json:"text_content,omitempty"`
	Thought                string                  `json:"thought,omitempty"`
	ToolCall               *MessageToolCall        `json:"tool_call,omitempty"`
	ReasoningContent       string                  `json:"reasoning_content,omitempty"`
	ThinkingBlocks         []ThinkingBlock         `json:"thinking_blocks,omitempty"`
	RedactedThinkingBlocks []RedactedThinkingBlock `json:"redacted_thinking_blocks,omitempty"`
	ResponsesReasoningItem *ReasoningItemModel     `json:"responses_reasoning_item,omitempty"`
}

// GetBase 返回嵌套的事件信封指针。
func (e *ActionEvent) GetBase() *BaseEvent { return &e.BaseEvent }
// Kind 返回事件类型标签，供 Controller 的类型 switch 使用。
func (e *ActionEvent) Kind() EventKind     { return KindAction }
// Name 返回人类可读的动作名称（如 run / read / finish）。
func (e *ActionEvent) Name() string        { return string(e.ActionType) }

// ToMessage 将 ActionEvent 转换为 assistant 角色的 LLM 对话消息，
// 携带工具调用和推理内容；若内容为空则返回零值 Message。
func (e *ActionEvent) ToMessage() Message {
	if e == nil {
		return Message{}
	}

	msg := Message{
		Role:      RoleAssistant,
		ToolCalls: []MessageToolCall{},
	}
	if e.ToolCall != nil {
		msg.ToolCalls = append(msg.ToolCalls, *e.ToolCall)
	}
	if e.Content.Text != "" {
		msg.Content = append(msg.Content, e.Content)
	}
	if e.ReasoningContent != "" {
		msg.ReasoningContent = e.ReasoningContent
	}
	if len(e.ThinkingBlocks) > 0 {
		msg.ThinkingBlocks = e.ThinkingBlocks
	}
	if len(e.RedactedThinkingBlocks) > 0 {
		msg.RedactedThinkingBlocks = e.RedactedThinkingBlocks
	}
	if e.ResponsesReasoningItem != nil {
		msg.ResponsesReasoningItem = e.ResponsesReasoningItem
	}

	if len(msg.ToolCalls) == 0 &&
		len(msg.Content) == 0 &&
		msg.ReasoningContent == "" &&
		len(msg.ThinkingBlocks) == 0 &&
		len(msg.RedactedThinkingBlocks) == 0 &&
		msg.ResponsesReasoningItem == nil {
		return Message{}
	}

	return msg
}
