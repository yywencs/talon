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

// GetBase 返回嵌套的事件信封指针。
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

// Kind 返回事件类型标签。
func (e *ObservationEvent) Kind() EventKind { return KindObservation }
// Name 返回固定的事件名称。
func (e *ObservationEvent) Name() string    { return "observation_event" }

// ToMessage 将 ObservationEvent 转换为 tool 角色的 LLM 对话消息，
// 用于回传工具执行结果。当内容为空时填充默认成功文本，
// 当存在 RejectionReason 时覆盖为拒绝原因。
func (e *ObservationEvent) ToMessage() Message {
	if e == nil {
		return Message{}
	}

	content := []Content(nil)
	if e.Observation != nil {
		content = e.Observation.GetContent()
	}
	if len(content) == 0 {
		content = []Content{
			TextContent{Text: "Command executed successfully with no output."},
		}
	}

	msg := Message{
		Role:       RoleTool,
		Content:    content,
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
