package types

import (
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
type EventKind = string

const (
	KindAction      EventKind = "action"
	KindObservation EventKind = "observation"
)

// BaseEvent 仅承载事件信封所需的最小元信息。
type BaseEvent struct {
	ID        string      `json:"id"`
	Timestamp time.Time   `json:"timestamp"`
	Source    EventSource `json:"source"`
}

func (b *BaseEvent) GetID() string {
	if b == nil {
		return ""
	}
	return b.ID
}

func (b *BaseEvent) GetTimestamp() time.Time {
	if b == nil {
		return time.Time{}
	}
	return b.Timestamp
}

func (b *BaseEvent) GetSource() EventSource {
	if b == nil {
		return ""
	}
	return b.Source
}

// Event 是所有事件的统一接口，任何在 EventBus 上流动的对象必须实现此接口。
type Event interface {
	GetID() string
	GetTimestamp() time.Time
	GetSource() EventSource
	Kind() string
	Name() string
}

// Messager 接口由能够转换为 LLM 对话消息的事件实现。
// ActionEvent 和 ObservationEvent 通过 ToMessage() 方法提供 LLM 可消费的格式。
type Messager interface {
	ToMessage() Message
}
