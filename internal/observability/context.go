package observability

import "context"

// TraceIDFromContext 返回当前 CozeLoop Span 的 trace_id。
func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	client, _, _ := providerSnapshot()
	if client == nil {
		return ""
	}
	return client.GetSpanFromContext(ctx).GetTraceID()
}

// SpanIDFromContext 返回当前 CozeLoop Span 的 span_id。
func SpanIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	client, _, _ := providerSnapshot()
	if client == nil {
		return ""
	}
	return client.GetSpanFromContext(ctx).GetSpanID()
}
