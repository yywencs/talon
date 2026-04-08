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
	Event
	isAction()
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

// CmdRunAction 表示需要在环境中执行的一条 shell 命令。
// 这是当前唯一会挂起 PendingAction 的 Action 类型。
// Thought 字段承载 Agent 的内部推理说明，供人工审查用，不会发给 LLM。
type CmdRunAction struct {
	BaseEvent
	Command  string `json:"command"`
	Thought  string `json:"thought,omitempty"`
	Blocking bool   `json:"blocking,omitempty"`
}

func (e *CmdRunAction) GetBase() *BaseEvent { return &e.BaseEvent }
func (e *CmdRunAction) Kind() EventKind     { return KindAction }
func (e *CmdRunAction) Name() string        { return string(ActionRun) }
func (e *CmdRunAction) isAction()           {}

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
func (e *MessageAction) isAction()           {}

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
func (e *FinishAction) isAction()           {}
