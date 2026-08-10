package observability

import (
	"fmt"
	"sync"

	otelcodes "go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// Span 表示业务层可操作的链路 Span。
type Span interface {
	End()
	SetStatus(status SpanStatus, description string)
	RecordError(err error, status SpanStatus)
	AddEvent(name string, attrs ...Attribute)
	SetAttributes(attrs ...Attribute)
	TraceID() string
	SpanID() string
}

type spanWrapper struct {
	span oteltrace.Span
	once sync.Once
}

type noopSpan struct{}

func newSpanWrapper(span oteltrace.Span) *spanWrapper {
	return &spanWrapper{span: span}
}

func newNoopSpan() *noopSpan {
	return &noopSpan{}
}

// End 结束 Span。
func (s *spanWrapper) End() {
	if s == nil || s.span == nil {
		return
	}
	s.once.Do(func() {
		s.span.End()
	})
}

// SetStatus 设置 Span 状态。
func (s *spanWrapper) SetStatus(status SpanStatus, description string) {
	if s == nil || s.span == nil {
		return
	}
	s.span.SetAttributes(toOTelAttributes([]Attribute{
		String(AttrSpanStatus, string(normalizeSpanStatus(status))),
		String(AttrErrorCategory, string(normalizeSpanStatus(status))),
	})...)
	s.span.SetStatus(toOTelStatusCode(status), description)
}

// RecordError 记录错误并更新 Span 状态。
func (s *spanWrapper) RecordError(err error, status SpanStatus) {
	if s == nil || s.span == nil || err == nil {
		return
	}
	s.span.RecordError(err)
	s.span.SetAttributes(toOTelAttributes([]Attribute{
		String(AttrErrorType, fmt.Sprintf("%T", err)),
		String(AttrErrorMessage, err.Error()),
		String(AttrErrorCategory, string(normalizeSpanStatus(status))),
	})...)
	s.SetStatus(status, err.Error())
}

// AddEvent 为 Span 增加事件。
func (s *spanWrapper) AddEvent(name string, attrs ...Attribute) {
	if s == nil || s.span == nil || name == "" {
		return
	}
	s.span.AddEvent(name, oteltrace.WithAttributes(toOTelAttributes(attrs)...))
}

// SetAttributes 批量设置属性。
func (s *spanWrapper) SetAttributes(attrs ...Attribute) {
	if s == nil || s.span == nil || len(attrs) == 0 {
		return
	}
	s.span.SetAttributes(toOTelAttributes(attrs)...)
}

// TraceID 返回当前 Span 的 trace_id。
func (s *spanWrapper) TraceID() string {
	if s == nil || s.span == nil {
		return ""
	}
	return s.span.SpanContext().TraceID().String()
}

// SpanID 返回当前 Span 的 span_id。
func (s *spanWrapper) SpanID() string {
	if s == nil || s.span == nil {
		return ""
	}
	return s.span.SpanContext().SpanID().String()
}

func (s *noopSpan) End() {}

func (s *noopSpan) SetStatus(status SpanStatus, description string) {
	_ = status
	_ = description
}

func (s *noopSpan) RecordError(err error, status SpanStatus) {
	_ = err
	_ = status
}

func (s *noopSpan) AddEvent(name string, attrs ...Attribute) {
	_ = name
	_ = attrs
}

func (s *noopSpan) SetAttributes(attrs ...Attribute) {
	_ = attrs
}

func (s *noopSpan) TraceID() string {
	return ""
}

func (s *noopSpan) SpanID() string {
	return ""
}

func toOTelStatusCode(status SpanStatus) otelcodes.Code {
	switch normalizeSpanStatus(status) {
	case SpanStatusOK:
		return otelcodes.Ok
	case SpanStatusError, SpanStatusTimeout, SpanStatusCancelled, SpanStatusRejected, SpanStatusPanicRecovered, SpanStatusLLMInvalidResponse:
		return otelcodes.Error
	default:
		return otelcodes.Error
	}
}
