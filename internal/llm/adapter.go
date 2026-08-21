// Package llm 负责把部署配置转换为 Eino 可以使用的模型组件。
package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	claudeadapter "github.com/cloudwego/eino-ext/components/model/claude"
	openaiadapter "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/wen/opentalon/internal/config"
)

const defaultRequestTimeout = 120 * time.Second

// defaultAnthropicMaxTokens 是 Anthropic Messages API 必填的 max_tokens。
// Agent 单轮回复由结构化 Intent 或简短说明构成，8192 足够且远低于模型上限。
const defaultAnthropicMaxTokens = 8192

type options struct {
	httpClient *http.Client
}

// Option 配置模型适配器的可选能力。
type Option func(*options)

// WithHTTPClient 注入自定义 HTTP Client，可用于代理、mTLS、链路追踪或测试。
// Client 的超时和 Transport 生命周期由调用方负责管理。
func WithHTTPClient(client *http.Client) Option {
	return func(target *options) { target.httpClient = client }
}

// NewChatModel 根据部署配置创建支持工具调用的 Eino ChatModel。
// openai-compatible 可用于任何兼容 OpenAI Chat Completions API 的外部服务；
// anthropic-compatible 可用于任何兼容 Anthropic Messages API 的外部服务
// （如智谱 BigModel 的 /api/anthropic 通道，Coding Plan 权益绑定在该协议）；
// ollama 作为兼容别名保留，并在 Endpoint 未包含 /v1 时自动补全。
func NewChatModel(ctx context.Context, cfg config.LLMConfig, opts ...Option) (model.ToolCallingChatModel, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if provider == "" {
		return nil, fmt.Errorf("LLM provider is required")
	}
	modelName := strings.TrimSpace(cfg.Model)
	if modelName == "" {
		return nil, fmt.Errorf("LLM model is required")
	}

	endpoint, err := endpointForProvider(provider, cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	settings := options{}
	for _, option := range opts {
		if option != nil {
			option(&settings)
		}
	}
	if provider == "anthropic-compatible" {
		chatModel, err := claudeadapter.NewChatModel(ctx, &claudeadapter.Config{
			APIKey:    strings.TrimSpace(cfg.APIKey),
			BaseURL:   &endpoint,
			Model:     modelName,
			MaxTokens: defaultAnthropicMaxTokens,
		})
		if err != nil {
			return nil, fmt.Errorf("create Eino %s chat model: %w", provider, err)
		}
		return chatModel, nil
	}
	chatModel, err := openaiadapter.NewChatModel(ctx, &openaiadapter.ChatModelConfig{
		APIKey:     strings.TrimSpace(cfg.APIKey),
		BaseURL:    endpoint,
		Model:      modelName,
		Timeout:    defaultRequestTimeout,
		HTTPClient: settings.httpClient,
	})
	if err != nil {
		return nil, fmt.Errorf("create Eino %s chat model: %w", provider, err)
	}
	return chatModel, nil
}

func endpointForProvider(provider, value string) (string, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(value), "/")
	switch provider {
	case "openai":
		// Endpoint 为空时，Eino OpenAI 组件使用官方 API 地址；非空时也可接入网关。
	case "openai-compatible":
		if endpoint == "" {
			return "", fmt.Errorf("LLM endpoint is required for provider %q", provider)
		}
	case "anthropic-compatible":
		if endpoint == "" {
			return "", fmt.Errorf("LLM endpoint is required for provider %q", provider)
		}
	case "ollama":
		if endpoint == "" {
			endpoint = "http://localhost:11434"
		}
		if !strings.HasSuffix(endpoint, "/v1") {
			endpoint += "/v1"
		}
	default:
		return "", fmt.Errorf("unsupported LLM provider %q", provider)
	}
	if endpoint == "" {
		return "", nil
	}
	parsed, err := url.ParseRequestURI(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("invalid LLM endpoint %q: must be an absolute HTTP(S) URL", endpoint)
	}
	return endpoint, nil
}
