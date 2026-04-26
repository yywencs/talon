package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/observability"
)

// openAICompatibleClient 实现 OpenAI chat/completions 协议。
// 支持 DashScope、SiliconFlow、AnyProxy 等所有兼容 /chat/completions 的后端。
//
// Authorization header 使用统一的 Bearer token 语义，
// 因此大多数 OpenAI-compatible provider 都可以复用这一实现。
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

type openAIWireMessage struct {
	Role             string                    `json:"role"`
	Content          string                    `json:"content"`
	ToolCalls        []types.ChatToolCallInput `json:"tool_calls,omitempty"`
	ReasoningContent string                    `json:"reasoning_content,omitempty"`
}

// openAIWireResponse 是 OpenAI-compatible 接口返回的 wire 协议响应。
type openAIWireResponse struct {
	Choices []struct {
		Message openAIWireMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type openAIStreamChunk struct {
	Choices []struct {
		Delta struct {
			Role             string `json:"role"`
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function *struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function,omitempty"`
			} `json:"tool_calls,omitempty"`
		} `json:"delta"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Chat 向 OpenAI-compatible 接口发起 chat 请求。
func (c *openAICompatibleClient) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	endpoint := resolveOpenAIEndpoint(c.endpoint)
	ctx, span := startLLMRequestSpan(ctx, "openai", req.Model, endpoint, false)
	defer span.End()

	wireReq := openAIWireRequest{
		Model:       req.Model,
		Temperature: req.Temperature,
		Stream:      false,
		Tools:       convertToolsToAny(req.Tools),
	}
	messages, err := serializeOpenAIChatMessages(req.Messages)
	if err != nil {
		span.RecordError(err, observability.SpanStatusError)
		return nil, fmt.Errorf("序列化消息失败: %w", err)
	}
	wireReq.Messages = messages

	var wireResp openAIWireResponse
	err = doJSONRequest(ctx, sharedLLMHTTPClient, endpoint, wireReq, c.requestHeaders(), &wireResp)
	if err != nil {
		span.RecordError(err, observability.StatusFromError(err))
		return nil, fmt.Errorf("请求 openai-compatible 接口失败: %w", err)
	}

	if len(wireResp.Choices) == 0 {
		respErr := fmt.Errorf("openai-compatible 响应缺少 choices")
		span.RecordError(respErr, observability.SpanStatusLLMInvalidResponse)
		return nil, respErr
	}

	message, err := messageFromOpenAIChoice(wireResp.Choices[0].Message)
	if err != nil {
		span.RecordError(err, observability.SpanStatusLLMInvalidResponse)
		return nil, fmt.Errorf("解析 openai-compatible 消息失败: %w", err)
	}

	span.SetAttributes(
		observability.Int("llm.usage.input_tokens", wireResp.Usage.PromptTokens),
		observability.Int("llm.usage.output_tokens", wireResp.Usage.CompletionTokens),
	)
	span.SetStatus(observability.SpanStatusOK, "llm request completed")
	return &ChatResponse{
		Message:          message,
		PromptTokens:     wireResp.Usage.PromptTokens,
		CompletionTokens: wireResp.Usage.CompletionTokens,
	}, nil
}

func (c *openAICompatibleClient) StreamChat(ctx context.Context, req ChatRequest, onToken func(string)) (*ChatResponse, error) {
	endpoint := resolveOpenAIEndpoint(c.endpoint)
	ctx, span := startLLMRequestSpan(ctx, "openai", req.Model, endpoint, true)
	defer span.End()
	streamCtx, streamSpan := observability.TracerFor("internal/agent/llm_client").StartSpan(ctx, "llm.stream",
		observability.WithSpanKind(observability.SpanKindInternal),
	)
	defer streamSpan.End()

	wireReq := openAIWireRequest{
		Model:       req.Model,
		Temperature: req.Temperature,
		Stream:      true,
		Tools:       convertToolsToAny(req.Tools),
	}
	messages, err := serializeOpenAIChatMessages(req.Messages)
	if err != nil {
		span.RecordError(err, observability.SpanStatusError)
		return nil, fmt.Errorf("序列化消息失败: %w", err)
	}
	wireReq.Messages = messages

	body, err := json.Marshal(wireReq)
	if err != nil {
		span.RecordError(err, observability.SpanStatusError)
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(streamCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		span.RecordError(err, observability.SpanStatusError)
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}
	for k, v := range c.requestHeaders() {
		httpReq.Header.Set(k, v)
	}

	resp, err := sharedLLMHTTPClient.Do(httpReq)
	if err != nil {
		span.RecordError(err, observability.StatusFromError(err))
		return nil, fmt.Errorf("请求 openai-compatible 失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		recordCompletedStreamPayloads(ctx, span, body, buildStreamHTTPErrorPayload(resp.StatusCode, respBytes), "error",
			observability.Int("llm.response.status_code", resp.StatusCode),
		)
		err := fmt.Errorf("OpenAI-compatible 流式请求失败，状态码: %d，响应: %s", resp.StatusCode, string(respBytes))
		span.RecordError(err, observability.SpanStatusError)
		return nil, err
	}

	reader := bufio.NewReader(resp.Body)
	var contentBuilder strings.Builder
	role := string(types.RoleAssistant)
	reasoningContent := ""
	var toolCalls []types.MessageToolCall
	var promptTokens int
	var completionTokens int
	firstTokenEventSent := false
	streamToolCalls := make(map[int]*types.MessageToolCall)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			partialResp := &ChatResponse{
				Message:          buildAssistantMessage(role, contentBuilder.String(), flattenStreamToolCalls(streamToolCalls), reasoningContent),
				PromptTokens:     promptTokens,
				CompletionTokens: completionTokens,
			}
			recordCompletedStreamPayloads(ctx, span, body, buildStreamReadErrorPayloadFromResponse(partialResp, err), "error",
				observability.Int("llm.response.status_code", resp.StatusCode),
			)
			span.RecordError(err, observability.StatusFromError(err))
			return nil, fmt.Errorf("读取流式响应失败: %w", err)
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
			contentBuilder.WriteString(chunk.Choices[0].Delta.Content)
			if !firstTokenEventSent {
				firstTokenEventSent = true
				streamSpan.AddEvent("first_token_received", observability.Int("token.length", len(chunk.Choices[0].Delta.Content)))
			}
			onToken(chunk.Choices[0].Delta.Content)
		}
		if len(chunk.Choices) > 0 {
			mergeOpenAIStreamDelta(&role, &reasoningContent, streamToolCalls, chunk.Choices[0].Delta)
		}
		if chunk.Usage.PromptTokens > 0 {
			promptTokens = chunk.Usage.PromptTokens
		}
		if chunk.Usage.CompletionTokens > 0 {
			completionTokens = chunk.Usage.CompletionTokens
		}
	}

	toolCalls = flattenStreamToolCalls(streamToolCalls)

	streamSpan.SetAttributes(
		observability.Int("llm.usage.input_tokens", promptTokens),
		observability.Int("llm.usage.output_tokens", completionTokens),
		observability.Int("llm.tool_call.count", len(toolCalls)),
	)
	streamSpan.AddEvent("stream.completed",
		observability.Int("llm.usage.input_tokens", promptTokens),
		observability.Int("llm.usage.output_tokens", completionTokens),
		observability.Int("llm.tool_call.count", len(toolCalls)),
	)
	streamSpan.SetStatus(observability.SpanStatusOK, "llm stream completed")
	finalResp := &ChatResponse{
		Message:          buildAssistantMessage(role, contentBuilder.String(), toolCalls, reasoningContent),
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
	}
	recordCompletedStreamPayloads(ctx, span, body, buildStreamSuccessPayload(finalResp), "response",
		observability.Int("llm.response.status_code", resp.StatusCode),
	)
	span.SetAttributes(
		observability.Int("llm.usage.input_tokens", promptTokens),
		observability.Int("llm.usage.output_tokens", completionTokens),
		observability.Int("llm.tool_call.count", len(toolCalls)),
	)
	span.SetStatus(observability.SpanStatusOK, "llm request completed")
	return finalResp, nil
}

func (c *openAICompatibleClient) requestHeaders() map[string]string {
	headers := map[string]string{
		"Content-Type": "application/json",
	}
	if c.apiKey != "" {
		headers["Authorization"] = "Bearer " + c.apiKey
	}
	return headers
}

func mergeOpenAIStreamDelta(role *string, reasoningContent *string, streamToolCalls map[int]*types.MessageToolCall, delta struct {
	Role             string `json:"role"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content"`
	ToolCalls        []struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function *struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function,omitempty"`
	} `json:"tool_calls,omitempty"`
}) {
	if delta.Role != "" {
		*role = delta.Role
	}
	if delta.ReasoningContent != "" {
		*reasoningContent += delta.ReasoningContent
	}
	for _, toolCall := range delta.ToolCalls {
		existing := streamToolCalls[toolCall.Index]
		if existing == nil {
			existing = &types.MessageToolCall{}
			streamToolCalls[toolCall.Index] = existing
		}
		if toolCall.ID != "" {
			existing.ID = toolCall.ID
		}
		if toolCall.Function != nil {
			if toolCall.Function.Name != "" {
				existing.Name = toolCall.Function.Name
			}
			if toolCall.Function.Arguments != "" {
				existing.Arguments += toolCall.Function.Arguments
			}
		}
	}
}
