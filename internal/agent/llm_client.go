// Package agent 实现了与不同 LLM provider 交互的客户端封装。
// 当前支持两类后端：Ollama 原生 /api/chat 接口，以及 OpenAI-compatible /chat/completions 接口。
// 通过统一接口 LLMClient 抽象 provider 差异，使上层 agent 无需感知具体实现。
package agent

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/config"
	"github.com/wen/opentalon/pkg/observability"
)

// ChatRequest 是发给 LLM 的请求结构，Model/Messages 为必填字段，
// Temperature 控制随机性。
type ChatRequest struct {
	Model       string
	Messages    []types.Message
	Temperature float64
	Stream      bool
	Tools       []map[string]any // 新增：function calling tools
}

// ChatResponse 是 LLM 返回的响应结构，Message 为模型输出的统一消息格式，
// PromptTokens/CompletionTokens 分别统计输入/输出的 token 数量。
type ChatResponse struct {
	Message          types.Message
	PromptTokens     int
	CompletionTokens int
}

// LLMClient 接口抽象了所有 LLM provider 的 chat 能力。
type LLMClient interface {
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	StreamChat(ctx context.Context, req ChatRequest, onToken func(string)) (*ChatResponse, error)
}

var sharedLLMHTTPClient = &http.Client{
	Transport: http.DefaultTransport,
}

const (
	maxRetries  = 3
	baseBackoff = 500 * time.Millisecond
	maxBackoff  = 10 * time.Second
)

// newLLMClient 是工厂函数，按 config.LLMConfig 返回具体 client 实例。
// 之所以在这里做分发，而不是让外部直接 newOllamaClient/newOpenAIClient，
// 是因为 agent.go 只需要一个统一的 client，不需要知道背后是哪个 provider。
func NewLLMClient(cfg config.LLMConfig) (LLMClient, error) {
	switch normalizeProvider(cfg.Provider) {
	case "ollama":
		return newOllamaClient(cfg.Endpoint), nil
	case "openai":
		return newOpenAIClient(cfg.Endpoint, cfg.APIKey), nil
	default:
		return nil, fmt.Errorf("不支持的 LLM provider: %q", cfg.Provider)
	}
}

// normalizeProvider 将各种 provider 字符串归一化为内部标识符。
// 例如 "openai_compatible" / "dashscope" 都映射为 "openai"，
// 空字符串或 "ollama" 映射为 "ollama"。
func normalizeProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", "ollama":
		return "ollama"
	case "openai", "openai-compatible", "openai_compatible", "dashscope":
		return "openai"
	default:
		return strings.ToLower(strings.TrimSpace(provider))
	}
}

func startLLMRequestSpan(ctx context.Context, provider, model, endpoint string, stream bool) (context.Context, observability.Span) {
	return observability.TracerFor("internal/agent/llm_client").StartSpan(ctx, "llm.request",
		observability.WithSpanKind(observability.SpanKindClient),
		observability.WithAttributes(
			observability.String("llm.provider", provider),
			observability.String("llm.model", model),
			observability.String("llm.endpoint", endpoint),
			observability.Bool("llm.request.stream", stream),
		),
	)
}
