package types

import "fmt"

// Observation 表示工具或环境产出的业务结果。
// 核心引擎只通过它判断是否报错，并读取多模态内容。
type Observation interface {
	GetContent() []Content
	IsError() bool
}

// BaseObservation 是 Observation 的基础实现。
type BaseObservation struct {
	BaseEvent
	Content     []Content `json:"content"`
	ErrorStatus bool      `json:"error_status,omitempty"`
}

func (o *BaseObservation) GetContent() []Content {
	if o == nil {
		return nil
	}
	return o.Content
}

func (o *BaseObservation) IsError() bool {
	if o == nil {
		return true
	}
	return o.ErrorStatus
}

// ObservationEvent 是 Observation 的事件信封，用于在总线上传递系统元信息。
type ObservationEvent struct {
	BaseEvent
	ActionID    string      `json:"action_id"`
	ToolName    string      `json:"tool_name"`
	Observation Observation `json:"observation"`

	RejectionReason string `json:"rejection_reason,omitempty"`
	ToolCallID      string `json:"tool_call_id,omitempty"`
}

func (e *ObservationEvent) GetBase() *BaseEvent { return &e.BaseEvent }
func (e *ObservationEvent) Kind() EventKind     { return KindObservation }
func (e *ObservationEvent) Name() string        { return "observation_event" }

func (e *ObservationEvent) ToMessage() Message {
	msg := Message{
		Role:       RoleTool,
		ToolCallID: e.ToolCallID,
		Name:       e.ToolName,
	}
	if e.RejectionReason != "" {
		msg.Content = []Content{
			TextContent{
				Text: fmt.Sprintf("Action rejected: %s", e.RejectionReason),
			},
		}
	}
	return msg
}
