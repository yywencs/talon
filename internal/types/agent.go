package types

import "context"

// AgentOutputKind 表示 Agent 流式输出的语义类型。
type AgentOutputKind string

const (
	AgentOutputMessage      AgentOutputKind = "message"
	AgentOutputMessageDelta AgentOutputKind = "message_delta"
)

// AgentOutput 表示 Agent 在一次推理过程中的增量语义输出。
// 该结构不携带事件信封元信息，由 Session 负责包装成标准事件。
type AgentOutput struct {
	Kind      AgentOutputKind
	Message   *Message
	TextDelta string
}

// AgentTurnResult 表示 Agent 单轮推理的最终结果。
// Message 用于展示最终助手消息，ToolCalls 用于后续生成 ActionEvent。
type AgentTurnResult struct {
	Message                *Message
	ToolCalls              []MessageToolCall
	ActionReasoningContent string
	Finished               bool
}

// Agent 定义会话执行时的智能体行为接口。
type Agent interface {
	// StreamStep 执行一次推理流程，并通过回调输出增量语义结果。
	StreamStep(ctx context.Context, state *SessionState, onOutput func(AgentOutput)) (*AgentTurnResult, error)
}
