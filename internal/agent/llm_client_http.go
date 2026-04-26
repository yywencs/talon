package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/logger"
	"github.com/wen/opentalon/pkg/observability"
)

type retryableError struct {
	StatusCode int
	Message    string
	RetryAfter time.Duration
}

func (e *retryableError) Error() string {
	return fmt.Sprintf("状态码 %d: %s", e.StatusCode, e.Message)
}

func (e *retryableError) IsRetryable() bool {
	return true
}

type nonRetryableError struct {
	StatusCode int
	Message    string
}

func (e *nonRetryableError) Error() string {
	return fmt.Sprintf("状态码 %d: %s", e.StatusCode, e.Message)
}

func (e *nonRetryableError) IsRetryable() bool {
	return false
}

func isRetryableStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests ||
		statusCode == http.StatusInternalServerError ||
		statusCode == http.StatusBadGateway ||
		statusCode == http.StatusServiceUnavailable ||
		statusCode == http.StatusGatewayTimeout
}

func isNonRetryableStatus(statusCode int) bool {
	return statusCode == http.StatusBadRequest ||
		statusCode == http.StatusUnauthorized ||
		statusCode == http.StatusForbidden ||
		statusCode == http.StatusNotFound ||
		statusCode == http.StatusUnprocessableEntity
}

// doJSONRequest 是通用的 JSON HTTP 请求封装，处理序列化、请求构建、响应解析。
// headers 参数可选，用于传入 Authorization 等自定义 header。
func doJSONRequest(ctx context.Context, client *http.Client, endpoint string, reqBody any, headers map[string]string, respBody any) error {
	return doRequestWithRetry(ctx, client, http.MethodPost, endpoint, reqBody, headers, respBody)
}

func doRequestWithRetry(ctx context.Context, client *http.Client, method, endpoint string, reqBody any, headers map[string]string, respBody any) error {
	span := observability.SpanFromContext(ctx)
	body, err := json.Marshal(reqBody)
	if err != nil {
		span.RecordError(err, observability.SpanStatusError)
		return fmt.Errorf("序列化请求失败: %w", err)
	}
	recordPayloadObservation(ctx, span, "llm.request", "request", body)

	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			backoff := calculateBackoff(attempt, lastErr)
			span.AddEvent("retry.scheduled",
				observability.Int("retry.attempt", attempt),
				observability.String("retry.backoff", backoff.String()),
			)
			select {
			case <-ctx.Done():
				span.RecordError(ctx.Err(), observability.StatusFromError(ctx.Err()))
				return ctx.Err()
			case <-time.After(backoff):
			}
		}

		httpReq, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
		if err != nil {
			span.RecordError(err, observability.SpanStatusError)
			return fmt.Errorf("创建请求失败: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			httpReq.Header.Set(k, v)
		}

		httpResp, err := client.Do(httpReq)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				span.RecordError(ctx.Err(), observability.StatusFromError(ctx.Err()))
				return ctx.Err()
			}
			continue
		}

		respBytes, readErr := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		if readErr != nil {
			span.RecordError(readErr, observability.SpanStatusError)
			lastErr = fmt.Errorf("读取响应失败: %w", readErr)
			continue
		}

		statusCode := httpResp.StatusCode
		recordPayloadObservation(ctx, span, "llm.response", "response", respBytes,
			observability.Int("llm.response.status_code", statusCode),
			observability.Int("llm.response.retry_attempt", attempt),
		)
		span.AddEvent("response.received",
			observability.Int("http.status_code", statusCode),
			observability.Int("retry.attempt", attempt),
		)

		if statusCode >= 200 && statusCode < 300 {
			if unmarshalErr := json.Unmarshal(respBytes, respBody); unmarshalErr != nil {
				span.RecordError(unmarshalErr, observability.SpanStatusLLMInvalidResponse)
				return fmt.Errorf("解析响应失败: %w", unmarshalErr)
			}
			return nil
		}

		respStr := strings.TrimSpace(string(respBytes))
		if isNonRetryableStatus(statusCode) {
			return &nonRetryableError{StatusCode: statusCode, Message: respStr}
		}

		if isRetryableStatus(statusCode) {
			retryAfter := parseRetryAfter(httpResp.Header)
			lastErr = &retryableError{StatusCode: statusCode, Message: respStr, RetryAfter: retryAfter}
			continue
		}

		lastErr = &nonRetryableError{StatusCode: statusCode, Message: respStr}
		break
	}

	return lastErr
}

func recordPayloadObservation(ctx context.Context, span observability.Span, prefix, kind string, payload any, extraAttrs ...observability.Attribute) {
	if span == nil {
		return
	}
	summary, err := observability.SummarizePayload(payload)
	if err != nil {
		logger.ErrorWithCtx(ctx, "payload 摘要生成失败",
			"prefix", prefix,
			"kind", kind,
			"error", err,
		)
		span.AddEvent("payload.summary_failed",
			observability.String("payload.prefix", prefix),
			observability.String("payload.kind", kind),
			observability.String("error.message", err.Error()),
		)
		return
	}

	artifactPath := ""
	artifactPath, err = observability.WritePayloadArtifact(ctx, kind, payload)
	if err != nil {
		logger.ErrorWithCtx(ctx, "payload artifact 写入失败",
			"prefix", prefix,
			"kind", kind,
			"error", err,
		)
		span.AddEvent("payload.artifact_write_failed",
			observability.String("payload.prefix", prefix),
			observability.String("payload.kind", kind),
			observability.String("error.message", err.Error()),
		)
	} else {
		span.AddEvent("payload.artifact_written",
			observability.String("payload.prefix", prefix),
			observability.String("payload.kind", kind),
			observability.String("payload.artifact_path", artifactPath),
		)
	}

	attrs := observability.PayloadSummaryAttributes(prefix, summary, artifactPath)
	attrs = append(attrs, extraAttrs...)
	span.SetAttributes(attrs...)
}

func recordCompletedStreamPayloads(ctx context.Context, span observability.Span, requestPayload any, responsePayload any, responseKind string, responseAttrs ...observability.Attribute) {
	recordPayloadObservation(ctx, span, "llm.request", "request", requestPayload)
	recordPayloadObservation(ctx, span, "llm.response", responseKind, responsePayload, responseAttrs...)
}

func buildStreamSuccessPayload(resp *ChatResponse) map[string]any {
	if resp == nil {
		return map[string]any{
			"message":           nil,
			"prompt_tokens":     0,
			"completion_tokens": 0,
		}
	}
	return map[string]any{
		"message":           resp.Message,
		"prompt_tokens":     resp.PromptTokens,
		"completion_tokens": resp.CompletionTokens,
	}
}

func buildStreamHTTPErrorPayload(statusCode int, respBytes []byte) map[string]any {
	return map[string]any{
		"status_code": statusCode,
		"body":        json.RawMessage(respBytes),
	}
}

func buildStreamReadErrorPayload(content string, promptTokens, completionTokens int, err error) map[string]any {
	return buildStreamReadErrorPayloadFromResponse(&ChatResponse{
		Message:          buildAssistantMessage(string(types.RoleAssistant), content, nil, ""),
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
	}, err)
}

func buildStreamReadErrorPayloadFromResponse(resp *ChatResponse, err error) map[string]any {
	payload := map[string]any{
		"error": err.Error(),
	}
	if resp != nil {
		payload["partial_response"] = buildStreamSuccessPayload(resp)
	}
	return payload
}

func calculateBackoff(attempt int, lastErr error) time.Duration {
	if re, ok := lastErr.(*retryableError); ok && re.RetryAfter > 0 {
		return re.RetryAfter
	}

	backoff := float64(baseBackoff) * math.Pow(2, float64(attempt-1))
	if backoff > float64(maxBackoff) {
		backoff = float64(maxBackoff)
	}

	jitter := time.Duration(rand.Float64() * 0.3 * backoff)
	return time.Duration(backoff) + jitter
}

func parseRetryAfter(header http.Header) time.Duration {
	v := header.Get("Retry-After")
	if v == "" {
		return 0
	}

	if seconds, err := strconv.Atoi(v); err == nil {
		return time.Duration(seconds) * time.Second
	}

	if t, err := time.Parse(http.TimeFormat, v); err == nil {
		wait := time.Until(t)
		if wait > 0 {
			return wait
		}
	}
	return 0
}

// resolveOllamaEndpoint 将基础 URL 补全为 /api/chat 端点。
// 允许两种传参方式：基础地址 http://host:11434 或完整路径 http://host:11434/api/chat。
// 前者更符合“配置一个地址”的直觉，后者给需要代理转发的场景留出口。
func resolveOllamaEndpoint(endpoint string) string {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		return "http://localhost:11434/api/chat"
	}
	if strings.HasSuffix(endpoint, "/api/chat") {
		return endpoint
	}
	return endpoint + "/api/chat"
}

// resolveOpenAIEndpoint 将基础 URL 补全为 /chat/completions 端点。
// DashScope 与其他 OpenAI-compatible provider 都遵循相同路径约定。
func resolveOpenAIEndpoint(endpoint string) string {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		return "https://api.openai.com/v1/chat/completions"
	}
	if strings.HasSuffix(endpoint, "/chat/completions") {
		return endpoint
	}
	if strings.HasSuffix(endpoint, "/v1") {
		return endpoint + "/chat/completions"
	}
	return endpoint + "/chat/completions"
}
