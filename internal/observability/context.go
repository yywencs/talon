package observability

import (
	"context"

	oteltrace "go.opentelemetry.io/otel/trace"
)

// TraceIDFromContext 返回当前上下文中的 trace_id。
func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	spanContext := oteltrace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.TraceID().String()
}

// SpanIDFromContext 返回当前上下文中的 span_id。
func SpanIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	spanContext := oteltrace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.SpanID().String()
}
