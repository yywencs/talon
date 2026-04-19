// Package types 定义了 Agent 系统内部使用的核心数据类型。
// 这些类型描述了系统中流动的所有事件（Event）、动作（Action）和观察结果（Observation）。
//
// 类型设计原则：
//   - 所有事件共享 BaseEvent，提供统一的 ID、Timestamp、Source 等元字段。
//   - Action 和 Observation 是两个互不重叠的接口，Controller 通过类型 switch 分发处理。
//   - PendingAction 机制要求每个 Action 的 ID 在整个会话内唯一，用于因果匹配。
package types

// Action 接口由所有动作类型实现，表示 Agent 向环境发出的指令或消息。
// Action 是"由 Agent 主动发起"的事件，与 Observation（被动接收）相对。
type Action interface {
	ActionType() ActionType
}

type SecurityRisk string

const (
	SecurityRisk_UNKNOWN SecurityRisk = "UNKNOWN"
	SecurityRisk_LOW     SecurityRisk = "LOW"
	SecurityRisk_MEDIUM  SecurityRisk = "MEDIUM"
	SecurityRisk_HIGH    SecurityRisk = "HIGH"
)

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

func (s SecurityRisk) IsRiskierOrEqual(other SecurityRisk) bool {
	if s == SecurityRisk_UNKNOWN || other == SecurityRisk_UNKNOWN {
		return false
	}
	return s.weight() >= other.weight()
}

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
	Action     Action     `json:"action"`
	ToolName   string     `json:"tool_name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`

	LLMResponseID string       `json:"llm_response_id,omitempty"`
	Summary       string       `json:"summary,omitempty"`
	SecurityRisk  SecurityRisk `json:"security_risk,omitempty"`

	Thought                string                  `json:"thought,omitempty"`
	ToolCall               *MessageToolCall        `json:"tool_call,omitempty"`
	ReasoningContent       string                  `json:"reasoning_content,omitempty"`
	ThinkingBlocks         []ThinkingBlock         `json:"thinking_blocks,omitempty"`
	RedactedThinkingBlocks []RedactedThinkingBlock `json:"redacted_thinking_blocks,omitempty"`
	ResponsesReasoningItem *ReasoningItemModel     `json:"responses_reasoning_item,omitempty"`
}

func (e *ActionEvent) GetBase() *BaseEvent { return &e.BaseEvent }
func (e *ActionEvent) Kind() EventKind     { return KindAction }
func (e *ActionEvent) Name() string        { return string(e.ActionType) }

func (e *ActionEvent) ToMessage() Message {
	msg := Message{
		Role:      RoleAssistant,
		ToolCalls: []MessageToolCall{},
	}
	if e.ToolCall != nil {
		msg.ToolCalls = append(msg.ToolCalls, *e.ToolCall)
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
	return msg
}
