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

// MessageAction 表示一条需要展示给用户的消息。
// WaitForResponse=true 表示这条消息期望用户回复后才能继续，通常用于询问 clarification。
// 注意：MessageAction 不会挂起 PendingAction，循环不会在发送后停止。
type MessageAction struct {
	BaseEvent
	Content         string `json:"content"`
	WaitForResponse bool   `json:"wait_for_response,omitempty"`
}

func (e *MessageAction) GetBase() *BaseEvent { return &e.BaseEvent }
func (e *MessageAction) Kind() EventKind     { return KindAction }
func (e *MessageAction) Name() string        { return string(ActionMessage) }
func (e *MessageAction) ActionType() ActionType {
	return ActionMessage
}

// FinishAction 表示任务已成功完成。
// Result 是可选的结果摘要，供外部调用方展示或记录用。
// FinishAction 不会挂起 PendingAction，即使在语义上它"结束"了因果链也不需要等待。
type FinishAction struct {
	BaseEvent
	Result string `json:"result,omitempty"`
}

func (e *FinishAction) GetBase() *BaseEvent { return &e.BaseEvent }
func (e *FinishAction) Kind() EventKind     { return KindAction }
func (e *FinishAction) Name() string        { return string(ActionFinish) }
func (e *FinishAction) ActionType() ActionType {
	return ActionFinish
}
