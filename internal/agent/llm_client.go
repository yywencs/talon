// Package agent 实现了与不同 LLM provider 交互的客户端封装。
// 当前支持两类后端：Ollama 原生 /api/chat 接口，以及 OpenAI-compatible /chat/completions 接口。
// 通过统一接口 llmClient 抽象 provider 差异，使上层 agent 无需感知具体实现。
package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wen/opentalon/pkg/config"
)

// llmChatMessage 表示对话中的一条消息，包含角色和内容。
// Role 支持 "user" / "assistant" / "system" 等标准角色。
type llmChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content,omitempty"`
}

// llmChatRequest 是发给 LLM 的请求结构，Model/Messages 为必填字段，
// Schema 用于声明输出 JSON schema（部分 provider 支持），Temperature 控制随机性。
type llmChatRequest struct {
	Model       string
	Messages    []llmChatMessage
	Schema      map[string]any
	Temperature float64
}

// llmChatResponse 是 LLM 返回的响应结构，Content 为实际文本回复，
// PromptTokens/CompletionTokens 分别统计输入/输出的 token 数量。
type llmChatResponse struct {
	Content          string
	PromptTokens     int
	CompletionTokens int
}

// llmClient 接口抽象了所有 LLM provider 的 chat 能力。
// 之所以用接口而不是直接分支，是因为 agent 需要在运行时按配置切换具体实现，
// 而不想在 agent.Step() 里写 if-else 判断要走哪条路。
type llmClient interface {
	Chat(req llmChatRequest) (*llmChatResponse, error)
}

// sharedLLMHTTPClient 是全局复用的 HTTP client，Timeout 设置为 2 分钟，
// 足够覆盖大多数 LLM 请求（包括模型冷启动时间）。
var sharedLLMHTTPClient = &http.Client{Timeout: 2 * time.Minute}

// newLLMClient 是工厂函数，按 config.LLMConfig 返回具体 client 实例。
// 之所以在这里做分发，而不是让外部直接 newOllamaClient/newOpenAIClient，
// 是因为 agent.go 只需要一个统一的 client，不需要知道背后是哪个 provider。
func newLLMClient(cfg config.LLMConfig) (llmClient, error) {
	switch normalizeProvider(cfg.Provider) {
	case "ollama":
		return &ollamaClient{
			endpoint: strings.TrimSpace(cfg.Endpoint),
			http:     sharedLLMHTTPClient,
		}, nil
	case "openai":
		return &openAICompatibleClient{
			endpoint: strings.TrimSpace(cfg.Endpoint),
			apiKey:   strings.TrimSpace(cfg.APIKey),
			http:     sharedLLMHTTPClient,
		}, nil
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

// ------------------------------------------------------------------
// ollamaClient 实现原生 Ollama /api/chat 接口。
// Ollama 原生协议使用 "format" 字段声明 JSON schema（而不是 tools 或 response_format），
// 且 metrics 字段为 prompt_eval_count / eval_count，与 OpenAI 体系完全不同。
// ------------------------------------------------------------------
type ollamaClient struct {
	endpoint string
	http     *http.Client
}

// ollamaWireRequest 是发给 Ollama 的 wire 协议请求。
type ollamaWireRequest struct {
	Model    string           `json:"model"`
	Messages []llmChatMessage `json:"messages"`
	Stream   bool             `json:"stream"`
	Format   map[string]any   `json:"format,omitempty"`
	Options  map[string]any   `json:"options,omitempty"`
}

// ollamaWireResponse 是 Ollama 返回的 wire 协议响应。
type ollamaWireResponse struct {
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	PromptEvalCount int `json:"prompt_eval_count"`
	EvalCount       int `json:"eval_count"`
}

// Chat 向 Ollama 发起 chat 请求。
func (c *ollamaClient) Chat(req llmChatRequest) (*llmChatResponse, error) {
	wireReq := ollamaWireRequest{
		Model:    req.Model,
		Messages: req.Messages,
		Stream:   false,
		Format:   req.Schema,
		Options: map[string]any{
			"temperature": req.Temperature,
		},
	}

	var wireResp ollamaWireResponse
	if err := doJSONRequest(c.http, resolveOllamaEndpoint(c.endpoint), wireReq, nil, &wireResp); err != nil {
		return nil, fmt.Errorf("请求 ollama 失败: %w", err)
	}

	return &llmChatResponse{
		Content:          wireResp.Message.Content,
		PromptTokens:     wireResp.PromptEvalCount,
		CompletionTokens: wireResp.EvalCount,
	}, nil
}

// ------------------------------------------------------------------
// openAICompatibleClient 实现 OpenAI chat/completions 协议。
// 支持 DashScope、SiliconFlow、AnyProxy 等所有兼容 /chat/completions 的后端。
//
// 降级策略（retry with json_object）：
// 有些后端不支持 response_format json_schema，会返回 400。
// 此时自动切换到 {type: "json_object"} 再重试一次；
// 如果两次都失败，说明 provider 本身不支持结构化输出，不在这里兜底。
//
// Authorization header：OpenAI 兼容协议统一用 Bearer token。
// DashScope 的 api_key 走同样的 Authorization 头，不需要特殊处理。
// ------------------------------------------------------------------
type openAICompatibleClient struct {
	endpoint string
	apiKey   string
	http     *http.Client
}

// openAIWireRequest 是发给 OpenAI-compatible 接口的 wire 协议请求。
type openAIWireRequest struct {
	Model          string           `json:"model"`
	Messages       []llmChatMessage `json:"messages"`
	Temperature    float64          `json:"temperature,omitempty"`
	ResponseFormat map[string]any   `json:"response_format,omitempty"`
	Stream         bool             `json:"stream"`
}

// openAIWireResponse 是 OpenAI-compatible 接口返回的 wire 协议响应。
type openAIWireResponse struct {
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Chat 向 OpenAI-compatible 接口发起 chat 请求。
func (c *openAICompatibleClient) Chat(req llmChatRequest) (*llmChatResponse, error) {
	headers := map[string]string{
		"Content-Type": "application/json",
	}
	if c.apiKey != "" {
		headers["Authorization"] = "Bearer " + c.apiKey
	}

	wireReq := openAIWireRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		Stream:      false,
		ResponseFormat: map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "agent_decision",
				"schema": req.Schema,
			},
		},
	}

	var wireResp openAIWireResponse
	err := doJSONRequest(c.http, resolveOpenAIEndpoint(c.endpoint), wireReq, headers, &wireResp)
	if err != nil {
		// Some OpenAI-compatible providers support json_object but not json_schema.
		wireReq.ResponseFormat = map[string]any{"type": "json_object"}
		if retryErr := doJSONRequest(c.http, resolveOpenAIEndpoint(c.endpoint), wireReq, headers, &wireResp); retryErr != nil {
			return nil, fmt.Errorf("请求 openai-compatible 接口失败: %w", err)
		}
	}

	if len(wireResp.Choices) == 0 {
		return nil, fmt.Errorf("openai-compatible 响应缺少 choices")
	}

	return &llmChatResponse{
		Content:          wireResp.Choices[0].Message.Content,
		PromptTokens:     wireResp.Usage.PromptTokens,
		CompletionTokens: wireResp.Usage.CompletionTokens,
	}, nil
}

// doJSONRequest 是通用的 JSON HTTP 请求封装，处理序列化、请求构建、响应解析。
// headers 参数可选，用于传入 Authorization 等自定义 header。
func doJSONRequest(client *http.Client, endpoint string, reqBody any, headers map[string]string, respBody any) error {
	body, err := json.Marshal(reqBody)

	if err != nil {
		return fmt.Errorf("序列化请求失败: %w", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	httpResp, err := client.Do(httpReq)

	if err != nil {
		return err
	}
	defer httpResp.Body.Close()

	respBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return fmt.Errorf("读取响应失败: %w", err)
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return fmt.Errorf("状态码 %d: %s", httpResp.StatusCode, strings.TrimSpace(string(respBytes)))
	}

	if err := json.Unmarshal(respBytes, respBody); err != nil {
		return fmt.Errorf("解析响应失败: %w", err)
	}
	return nil
}

// ------------------------------------------------------------------
// resolveOllamaEndpoint 将基础 URL 补全为 /api/chat 端点。
// 允许两种传参方式：基础地址 http://host:11434 或完整路径 http://host:11434/api/chat。
// 前者更符合"配置一个地址"的直觉，后者给需要代理转发的场景留出口。
// ------------------------------------------------------------------
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
// DashScope / OpenAI-compatible 都遵循相同的路径约定，因此可以共用同一逻辑。
// 同样支持两种传参方式。
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
