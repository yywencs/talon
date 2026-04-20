package observability

import (
	"context"
	"errors"
)

const (
	// AttrSpanStatus 表示当前 Span 的归一化状态。
	AttrSpanStatus = "opentalon.span.status"
	// AttrErrorType 表示错误类型。
	AttrErrorType = "error.type"
	// AttrErrorMessage 表示错误消息。
	AttrErrorMessage = "error.message"
	// AttrErrorCategory 表示错误分类。
	AttrErrorCategory = "error.category"
)

// SpanStatus 表示业务层定义的 Span 状态。
type SpanStatus string

const (
	// SpanStatusOK 表示执行成功。
	SpanStatusOK SpanStatus = "ok"
	// SpanStatusError 表示一般错误。
	SpanStatusError SpanStatus = "error"
	// SpanStatusTimeout 表示超时。
	SpanStatusTimeout SpanStatus = "timeout"
	// SpanStatusCancelled 表示被取消。
	SpanStatusCancelled SpanStatus = "cancelled"
	// SpanStatusRejected 表示被拒绝。
	SpanStatusRejected SpanStatus = "rejected"
	// SpanStatusPanicRecovered 表示 panic 已被恢复。
	SpanStatusPanicRecovered SpanStatus = "panic_recovered"
	// SpanStatusLLMInvalidResponse 表示模型返回非法响应。
	SpanStatusLLMInvalidResponse SpanStatus = "llm_invalid_response"
)

func normalizeSpanStatus(status SpanStatus) SpanStatus {
	switch status {
	case SpanStatusOK, SpanStatusError, SpanStatusTimeout, SpanStatusCancelled, SpanStatusRejected, SpanStatusPanicRecovered, SpanStatusLLMInvalidResponse:
		return status
	default:
		return SpanStatusError
	}
}

// StatusFromError 根据 error 推导 SpanStatus。
func StatusFromError(err error) SpanStatus {
	if err == nil {
		return SpanStatusOK
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return SpanStatusTimeout
	}
	if errors.Is(err, context.Canceled) {
		return SpanStatusCancelled
	}
	return SpanStatusError
}
