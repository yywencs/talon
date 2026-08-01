package types

// TokenUsage 记录单次 LLM API 调用的 token 用量，
// 数据来源：服务端响应中的 usage 字段。
// 该类型保留在 types 包中，因为 AgentTurnResult 等纯数据结构直接引用它。
type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
}

// Total 返回 prompt + completion 的 token 总数。
// 在 OpenAI 兼容的 provider 中，等价于 `total_tokens` 字段。
func (u TokenUsage) Total() int {
	return u.PromptTokens + u.CompletionTokens
}
