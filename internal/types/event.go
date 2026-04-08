package types

import (
	"encoding/json"
	"time"
)

// EventSource 定义事件的来源，用于区分不同发起方在状态转换中的不同语义。
// 设计意图：
//   - SourceUser：用户主动输入，触发 AgentState 从 Loading/AwaitingInput 切回 Running。
//   - SourceAgent：Agent 主动发起，不自动推进状态（除了 WaitForResponse 场景）。
//   - SourceEnvironment：环境被动返回，所有环境事件都会尝试解锁 PendingAction。
type EventSource string

const (
	SourceAgent       EventSource = "agent"
	SourceUser        EventSource = "user"
	SourceEnvironment EventSource = "environment"
)

// EventKind 用于在 Controller 的类型 switch 中快速过滤 Action 和 Observation。
// 与 interface + type switch 配合使用比用反射更高效，且编译期安全。
type EventKind string

const (
	KindAction      EventKind = "action"
	KindObservation EventKind = "observation"
)

// Metrics 记录 LLM 推理的资源消耗，可用于日志、成本审计或调优。
// PromptTokens 和 CompletionTokens 来自不同 provider 的指标字段，
// OpenAI 兼容用 usage.prompt_tokens，Ollama 用 prompt_eval_count。
// TotalCost 为预留字段，需要外部按模型单价计算后填入。
type Metrics struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalCost        float64 `json:"total_cost"`
}

// ToolCallMetadata 预留用于 function calling 场景，记录工具名和调用 ID。
// 当前未在 PendingAction 中使用，但已预埋以便后续支持多工具并发。
type ToolCallMetadata struct {
	ToolName string `json:"tool_name"`
	CallID   string `json:"call_id"`
}

// BaseEvent 是所有事件的公共字段容器，提供 ID、Cause、Timestamp、Source 等元数据。
//
// Cause（因果链）设计意图：
//   - Action 被发布时由 EventBus 分配唯一 ID。
//   - 环境返回 Observation 时必须携带 Cause = 对应 Action 的 ID。
//   - Controller 通过 Cause 匹配来解锁 PendingAction，确保"谁的结果消除谁的挂起"。
//
// 为什么不在 Action 构造时分配 ID？
//   - ID 的唯一性依赖全局递增计数器，这个状态不适合放在 Controller 或 Agent 里。
//   - 统一在 EventBus.Publish() 中分配是最小特权原则：谁也不需要"知道"当前 ID 是多少。
type BaseEvent struct {
	ID        int64       `json:"id"`
	Timestamp time.Time   `json:"timestamp"`
	Source    EventSource `json:"source"`
	Cause     int64       `json:"cause"`
	Message   string      `json:"message,omitempty"`

	Timeout time.Duration `json:"timeout,omitempty"`

	LLMMetrics       *Metrics          `json:"llm_metrics,omitempty"`
	ToolCallMetadata *ToolCallMetadata `json:"tool_call_metadata,omitempty"`
	ResponseID       string            `json:"response_id,omitempty"`
}

// Event 是所有事件的统一接口，任何在 EventBus 上流动的对象必须实现此接口。
type Event interface {
	GetBase() *BaseEvent
	Kind() EventKind
	Name() string
}

// EventEnvelope 是 Event 的 JSON 序列化格式，用于持久化或跨服务传输。
// 它与 Event 接口可以互转：Event -> EventEnvelope（序列化），EventEnvelope -> 具体类型（反序列化）。
// 这使得 History 可以被完整保存到磁盘，稍后恢复时再还原为具体 Action/Observation 对象。
type EventEnvelope struct {
	ID               int64             `json:"id,omitempty"`
	Timestamp        time.Time         `json:"timestamp,omitempty"`
	Source           EventSource       `json:"source,omitempty"`
	Cause            int64             `json:"cause,omitempty"`
	Message          string            `json:"message,omitempty"`
	Timeout          time.Duration     `json:"timeout,omitempty"`
	LLMMetrics       *Metrics          `json:"llm_metrics,omitempty"`
	ToolCallMetadata *ToolCallMetadata `json:"tool_call_metadata,omitempty"`
	ResponseID       string            `json:"response_id,omitempty"`

	Kind        EventKind        `json:"kind"`
	Action      *ActionType      `json:"action,omitempty"`
	Observation *ObservationType `json:"observation,omitempty"`

	Args    json.RawMessage `json:"args,omitempty"`
	Content string          `json:"content,omitempty"`
	Extras  json.RawMessage `json:"extras,omitempty"`
}
