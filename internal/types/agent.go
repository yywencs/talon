package types

// AgentOutputKind 表示 Agent 流式输出的语义类型。
type AgentOutputKind string

const (
	// AgentOutputMessage 表示单条完整消息的输出（预留，暂无生产者）。
	AgentOutputMessage      AgentOutputKind = "message"
	// AgentOutputMessageDelta 表示文本 token 的增量推送。
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
	Message                *Message          // 最终助手消息（可能为 nil）
	ToolCalls              []MessageToolCall // 待执行的函数调用列表
	ActionReasoningContent string            // 关联到 ActionEvent 的推理内容
	Finished               bool              // 是否触发了 finish 动作或无调用直接结束
	TokenUsage             TokenUsage        // 本轮 token 用量（prompt + completion）
}
