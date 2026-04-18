// Package agent 实现了与不同 LLM provider 交互的客户端封装。
// 当前支持两类后端：Ollama 原生 /api/chat 接口，以及 OpenAI-compatible /chat/completions 接口。
// 通过统一接口 LLMClient 抽象 provider 差异，使上层 agent 无需感知具体实现。
package agent

import (
	"bufio"
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

	"github.com/wen/opentalon/internal/serializer"
	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/config"
	"github.com/wen/opentalon/pkg/utils"
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

// ChatResponse 是 LLM 返回的响应结构，Content 为实际文本回复，
// PromptTokens/CompletionTokens 分别统计输入/输出的 token 数量。
type ChatResponse struct {
	Content          string
	ToolCalls        []types.MessageToolCall `json:"tool_calls,omitempty"`
	PromptTokens     int
	CompletionTokens int
}

// LLMClient 接口抽象了所有 LLM provider 的 chat 能力。
type LLMClient interface {
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	StreamChat(ctx context.Context, req ChatRequest, onToken func(string)) error
}

var sharedLLMHTTPClient = &http.Client{
	Transport: newTraceRoundTripper(http.DefaultTransport),
}

const (
	maxRetries  = 3
	baseBackoff = 500 * time.Millisecond
	maxBackoff  = 10 * time.Second
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

// convertToolsToAny 将 []map[string]any 转换为 []any
func convertToolsToAny(tools []map[string]any) []any {
	result := make([]any, len(tools))
	for i, tool := range tools {
		result[i] = tool
	}
	return result
}

// ------------------------------------------------------------------
// ollamaClient 实现原生 Ollama /api/chat 接口。
// Ollama 原生协议使用 "format" 字段声明 JSON schema（而不是 tools 或 response_format），
// 且 metrics 字段为 prompt_eval_count / eval_count，与 OpenAI 体系完全不同。
// ------------------------------------------------------------------
type ollamaClient struct {
	endpoint string
}

func newOllamaClient(endpoint string) *ollamaClient {
	return &ollamaClient{
		endpoint: strings.TrimSpace(endpoint),
	}
}

// ollamaWireRequest 是发给 Ollama 的 wire 协议请求。
type ollamaWireRequest struct {
	Model    string         `json:"model"`
	Messages []any          `json:"messages"`
	Stream   bool           `json:"stream"`
	Options  map[string]any `json:"options,omitempty"`
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

func toOllamaMessages(messages []types.Message) []any {
	out := make([]any, 0, len(messages))
	for _, msg := range messages {
		content := utils.FlattenTextContent(msg.Content)
		if msg.ToolCalls != nil && len(msg.ToolCalls) > 0 {
			content = ""
		}
		m := map[string]any{
			"role":    string(msg.Role),
			"content": content,
		}
		out = append(out, m)
	}
	return out
}

// Chat 向 Ollama 发起 chat 请求。
func (c *ollamaClient) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	wireReq := ollamaWireRequest{
		Model:    req.Model,
		Messages: toOllamaMessages(stripCacheControl(req.Messages)),
		Stream:   req.Stream,
		Options: map[string]any{
			"temperature": req.Temperature,
		},
	}

	var wireResp ollamaWireResponse
	if err := doJSONRequest(ctx, sharedLLMHTTPClient, resolveOllamaEndpoint(c.endpoint), wireReq, nil, &wireResp); err != nil {
		return nil, fmt.Errorf("请求 ollama 失败: %w", err)
	}

	return &ChatResponse{
		Content:          wireResp.Message.Content,
		PromptTokens:     wireResp.PromptEvalCount,
		CompletionTokens: wireResp.EvalCount,
	}, nil
}

func (c *ollamaClient) StreamChat(ctx context.Context, req ChatRequest, onToken func(string)) error {
	wireReq := ollamaWireRequest{
		Model:    req.Model,
		Messages: toOllamaMessages(stripCacheControl(req.Messages)),
		Stream:   true,
		Options: map[string]any{
			"temperature": req.Temperature,
		},
	}

	body, err := json.Marshal(wireReq)
	if err != nil {
		return fmt.Errorf("序列化请求失败: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, resolveOllamaEndpoint(c.endpoint), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := sharedLLMHTTPClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("请求 ollama 失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Ollama 流式请求失败，状态码: %d，响应: %s", resp.StatusCode, string(respBytes))
	}

	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("读取流式响应失败: %w", err)
		}

		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		var chunk ollamaStreamChunk
		if err := json.Unmarshal(line, &chunk); err != nil {
			continue
		}

		if chunk.Message.Content != "" {
			onToken(chunk.Message.Content)
		}

		if chunk.Done {
			break
		}
	}

	return nil
}

type ollamaStreamChunk struct {
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	Done            bool `json:"done"`
	PromptEvalCount int  `json:"prompt_eval_count,omitempty"`
	EvalCount       int  `json:"eval_count,omitempty"`
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
}

func newOpenAIClient(endpoint, apiKey string) *openAICompatibleClient {
	return &openAICompatibleClient{
		endpoint: strings.TrimSpace(endpoint),
		apiKey:   strings.TrimSpace(apiKey),
	}
}

// openAIWireRequest 是发给 OpenAI-compatible 接口的 wire 协议请求。
type openAIWireRequest struct {
	Model          string         `json:"model"`
	Messages       []any          `json:"messages"`
	Temperature    float64        `json:"temperature,omitempty"`
	ResponseFormat map[string]any `json:"response_format,omitempty"`
	Tools          []any          `json:"tools,omitempty"`
	Stream         bool           `json:"stream"`
}

func serializeOpenAIChatMessages(messages []types.Message) ([]any, error) {
	messageSerializer := &serializer.OpenAIChatSerializer{
		CacheEnabled:           true,
		VisionEnabled:          true,
		FunctionCallingEnabled: true,
		SendReasoningContent:   true,
	}
	return serializer.SerializeMessages(messageSerializer, messages)
}

// openAIWireResponse 是 OpenAI-compatible 接口返回的 wire 协议响应。
type openAIWireResponse struct {
	Choices []struct {
		Message struct {
			Role      string                  `json:"role"`
			Content   string                  `json:"content"`
			ToolCalls []types.MessageToolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Chat 向 OpenAI-compatible 接口发起 chat 请求。
func (c *openAICompatibleClient) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	headers := map[string]string{
		"Content-Type": "application/json",
	}
	if c.apiKey != "" {
		headers["Authorization"] = "Bearer " + c.apiKey
	}

	wireReq := openAIWireRequest{
		Model:       req.Model,
		Temperature: req.Temperature,
		Stream:      false,
		Tools:       convertToolsToAny(req.Tools), // 新增：传递工具
	}
	messages, err := serializeOpenAIChatMessages(req.Messages)
	if err != nil {
		return nil, fmt.Errorf("序列化消息失败: %w", err)
	}
	wireReq.Messages = messages

	var wireResp openAIWireResponse
	err = doJSONRequest(ctx, sharedLLMHTTPClient, resolveOpenAIEndpoint(c.endpoint), wireReq, headers, &wireResp)
	if err != nil {
		return nil, fmt.Errorf("请求 openai-compatible 接口失败: %w", err)
	}

	if len(wireResp.Choices) == 0 {
		return nil, fmt.Errorf("openai-compatible 响应缺少 choices")
	}

	toolCalls := wireResp.Choices[0].Message.ToolCalls

	return &ChatResponse{
		Content:          wireResp.Choices[0].Message.Content,
		ToolCalls:        toolCalls,
		PromptTokens:     wireResp.Usage.PromptTokens,
		CompletionTokens: wireResp.Usage.CompletionTokens,
	}, nil
}

func (c *openAICompatibleClient) StreamChat(ctx context.Context, req ChatRequest, onToken func(string)) error {
	headers := map[string]string{
		"Content-Type": "application/json",
	}
	if c.apiKey != "" {
		headers["Authorization"] = "Bearer " + c.apiKey
	}

	wireReq := openAIWireRequest{
		Model:       req.Model,
		Temperature: req.Temperature,
		Stream:      true,
		Tools:       convertToolsToAny(req.Tools), // 新增：传递工具
	}
	messages, err := serializeOpenAIChatMessages(req.Messages)
	if err != nil {
		return fmt.Errorf("序列化消息失败: %w", err)
	}
	wireReq.Messages = messages

	body, err := json.Marshal(wireReq)
	if err != nil {
		return fmt.Errorf("序列化请求失败: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, resolveOpenAIEndpoint(c.endpoint), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := sharedLLMHTTPClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("请求 openai-compatible 失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("OpenAI-compatible 流式请求失败，状态码: %d，响应: %s", resp.StatusCode, string(respBytes))
	}

	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("读取流式响应失败: %w", err)
		}

		line = bytes.TrimSpace(line)
		if len(line) == 0 || !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}

		data := bytes.TrimPrefix(line, []byte("data: "))
		if bytes.Equal(data, []byte("[DONE]")) {
			break
		}

		var chunk openAIStreamChunk
		if err := json.Unmarshal(data, &chunk); err != nil {
			continue
		}

		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			onToken(chunk.Choices[0].Delta.Content)
		}
	}

	return nil
}

type openAIStreamChunk struct {
	Choices []struct {
		Delta struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// doJSONRequest 是通用的 JSON HTTP 请求封装，处理序列化、请求构建、响应解析。
// headers 参数可选，用于传入 Authorization 等自定义 header。
func doJSONRequest(ctx context.Context, client *http.Client, endpoint string, reqBody any, headers map[string]string, respBody any) error {
	return doRequestWithRetry(ctx, client, http.MethodPost, endpoint, reqBody, headers, respBody)
}

func doRequestWithRetry(ctx context.Context, client *http.Client, method, endpoint string, reqBody any, headers map[string]string, respBody any) error {
	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("序列化请求失败: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			backoff := calculateBackoff(attempt, lastErr)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}

		httpReq, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
		if err != nil {
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
				return ctx.Err()
			}
			continue
		}

		respBytes, readErr := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("读取响应失败: %w", readErr)
			continue
		}

		statusCode := httpResp.StatusCode

		var respData map[string]any
		json.Unmarshal(respBytes, &respData)

		if statusCode >= 200 && statusCode < 300 {
			if unmarshalErr := json.Unmarshal(respBytes, respBody); unmarshalErr != nil {
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
// 前者更符合"配置一个地址"的直觉，后者给需要代理转发的场景留出口。
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

func stripCacheControl(messages []types.Message) []types.Message {
	if len(messages) == 0 {
		return nil
	}
	sanitized := make([]types.Message, len(messages))
	copy(sanitized, messages)
	for i := range sanitized {
		if len(sanitized[i].Content) == 0 {
			continue
		}
		sanitized[i].Content = cloneContentWithoutCache(sanitized[i].Content)
	}
	return sanitized
}

func cloneContentWithoutCache(contents []types.Content) []types.Content {
	cloned := make([]types.Content, 0, len(contents))
	for _, content := range contents {
		switch c := content.(type) {
		case types.TextContent:
			c.CachePrompt = false
			cloned = append(cloned, c)
		case *types.TextContent:
			if c == nil {
				continue
			}
			dup := *c
			dup.CachePrompt = false
			cloned = append(cloned, dup)
		case types.ImageContent:
			c.CachePrompt = false
			cloned = append(cloned, c)
		case *types.ImageContent:
			if c == nil {
				continue
			}
			dup := *c
			dup.CachePrompt = false
			cloned = append(cloned, dup)
		default:
			cloned = append(cloned, content)
		}
	}
	return cloned
}
