package observability

import (
	"context"

	oteltrace "go.opentelemetry.io/otel/trace"
)

// ContextWithSpan 将 Span 注入 context。
func ContextWithSpan(ctx context.Context, span Span) context.Context {
	wrapped, ok := span.(*spanWrapper)
	if !ok || wrapped == nil {
		return ctx
	}
	return oteltrace.ContextWithSpan(ctx, wrapped.span)
}

// SpanFromContext 从 context 中提取当前 Span。
func SpanFromContext(ctx context.Context) Span {
	if ctx == nil {
		return newNoopSpan()
	}
	span := oteltrace.SpanFromContext(ctx)
	if span == nil {
		return newNoopSpan()
	}
	spanCtx := span.SpanContext()
	if !spanCtx.IsValid() {
		return newNoopSpan()
	}
	return newSpanWrapper(span)
}

// TraceIDFromContext 返回当前上下文中的 trace_id。
func TraceIDFromContext(ctx context.Context) string {
	return SpanFromContext(ctx).TraceID()
}

// SpanIDFromContext 返回当前上下文中的 span_id。
func SpanIDFromContext(ctx context.Context) string {
	return SpanFromContext(ctx).SpanID()
}
