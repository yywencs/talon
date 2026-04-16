package types

import "strings"

// Observation 表示工具或环境产出的业务结果。
// 核心引擎只通过它判断是否报错，并读取多模态内容。
type Observation interface {
	GetContent() []Content
	IsError() bool
}

// ContentType 表示多模态内容的判别标签。
type ContentType string

const (
	ContentTypeText     ContentType = "text"
	ContentTypeImageURL ContentType = "image_url"
)

// Content 是多模态内容的联合类型接口。
type Content interface {
	Type() ContentType
}

// TextContent 表示纯文本内容。
type TextContent struct {
	DataType ContentType `json:"type"`
	Text     string      `json:"text"`
}

func (c TextContent) Type() ContentType { return c.DataType }

// ImageURL 表示图片 URL 载荷。
type ImageURL struct {
	URL string `json:"url"`
}

// ImageContent 表示图片内容。
type ImageContent struct {
	DataType ContentType `json:"type"`
	ImageURL ImageURL    `json:"image_url"`
}

func (c ImageContent) Type() ContentType { return c.DataType }

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

type ObservationType string

const (
	ObsRun     ObservationType = "run"
	ObsRead    ObservationType = "read"
	ObsWrite   ObservationType = "write"
	ObsEdit    ObservationType = "edit"
	ObsError   ObservationType = "error"
	ObsSuccess ObservationType = "success"
)

// ObservationEvent 是 Observation 的事件信封，用于在总线上传递系统元信息。
type ObservationEvent struct {
	BaseEvent
	ActionID    string      `json:"action_id"`
	ToolName    string      `json:"tool_name"`
	Observation Observation `json:"observation"`
}

func (e *ObservationEvent) GetBase() *BaseEvent { return &e.BaseEvent }
func (e *ObservationEvent) Kind() EventKind     { return KindObservation }
func (e *ObservationEvent) Name() string        { return "observation_event" }

// FlattenTextContent 将多模态内容中的文本片段按顺序拼接，便于日志或 prompt 构造。
func FlattenTextContent(contents []Content) string {
	var parts []string
	for _, item := range contents {
		text, ok := item.(TextContent)
		if ok && strings.TrimSpace(text.Text) != "" {
			parts = append(parts, text.Text)
			continue
		}

		if textPtr, ok := item.(*TextContent); ok && strings.TrimSpace(textPtr.Text) != "" {
			parts = append(parts, textPtr.Text)
		}
	}
	return strings.Join(parts, "\n")
}
