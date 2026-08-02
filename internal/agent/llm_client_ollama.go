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

// ollamaClient 实现原生 Ollama /api/chat 接口。
// Ollama 原生协议使用 "format" 字段声明 JSON schema（而不是 tools 或 response_format），
// 且 metrics 字段为 prompt_eval_count / eval_count，与 OpenAI 体系完全不同。
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
	Tools    []any          `json:"tools,omitempty"`
	Stream   bool           `json:"stream"`
	Options  map[string]any `json:"options,omitempty"`
}

type ollamaWireToolCall struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// ollamaWireResponse 是 Ollama 返回的 wire 协议响应。
type ollamaWireResponse struct {
	Message struct {
		Role      string               `json:"role"`
		Content   string               `json:"content"`
		ToolCalls []ollamaWireToolCall `json:"tool_calls,omitempty"`
	} `json:"message"`
	PromptEvalCount int `json:"prompt_eval_count"`
	EvalCount       int `json:"eval_count"`
}

type ollamaStreamChunk struct {
	Message struct {
		Role      string               `json:"role"`
		Content   string               `json:"content"`
		ToolCalls []ollamaWireToolCall `json:"tool_calls,omitempty"`
	} `json:"message"`
	Done            bool `json:"done"`
	PromptEvalCount int  `json:"prompt_eval_count,omitempty"`
	EvalCount       int  `json:"eval_count,omitempty"`
}

// Chat 向 Ollama 发起 chat 请求。
func (c *ollamaClient) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	endpoint := resolveOllamaEndpoint(c.endpoint)
	ctx, span := startLLMRequestSpan(ctx, "ollama", req.Model, endpoint, false)
	defer span.End()

	wireReq := ollamaWireRequest{
		Model:    req.Model,
		Messages: toOllamaMessages(stripCacheControl(req.Messages)),
		Tools:    convertToolsToAny(req.Tools),
		Stream:   req.Stream,
		Options: map[string]any{
			"temperature": req.Temperature,
		},
	}

	var wireResp ollamaWireResponse
	if err := doJSONRequest(ctx, sharedLLMHTTPClient, endpoint, wireReq, nil, &wireResp); err != nil {
		span.RecordError(err, observability.StatusFromError(err))
		return nil, fmt.Errorf("请求 ollama 失败: %w", err)
	}

	span.SetAttributes(
		observability.Int("llm.usage.input_tokens", wireResp.PromptEvalCount),
		observability.Int("llm.usage.output_tokens", wireResp.EvalCount),
	)
	span.SetStatus(observability.SpanStatusOK, "llm request completed")
	toolCalls, err := messageToolCallsFromOllama(wireResp.Message.ToolCalls)
	if err != nil {
		span.RecordError(err, observability.SpanStatusLLMInvalidResponse)
		return nil, err
	}
	return &ChatResponse{
		Message:          buildAssistantMessage(wireResp.Message.Role, wireResp.Message.Content, toolCalls, ""),
		PromptTokens:     wireResp.PromptEvalCount,
		CompletionTokens: wireResp.EvalCount,
	}, nil
}

func messageToolCallsFromOllama(toolCalls []ollamaWireToolCall) ([]types.MessageToolCall, error) {
	result := make([]types.MessageToolCall, 0, len(toolCalls))
	for index, toolCall := range toolCalls {
		if toolCall.Function.Name == "" {
			return nil, fmt.Errorf("ollama tool call %d has no function name", index)
		}
		arguments := strings.TrimSpace(string(toolCall.Function.Arguments))
		if arguments == "" || arguments == "null" {
			arguments = "{}"
		} else if strings.HasPrefix(arguments, `"`) {
			var decoded string
			if err := json.Unmarshal(toolCall.Function.Arguments, &decoded); err != nil {
				return nil, fmt.Errorf("decode ollama tool call %d arguments: %w", index, err)
			}
			arguments = decoded
		}
		callID := toolCall.ID
		if callID == "" {
			callID = fmt.Sprintf("call_ollama_%d", index)
		}
		result = append(result, types.MessageToolCall{
			ID: callID, Name: toolCall.Function.Name, Arguments: arguments, Origin: types.OriginCompletion,
		})
	}
	return result, nil
}

func (c *ollamaClient) StreamChat(ctx context.Context, req ChatRequest, onToken func(string)) (*ChatResponse, error) {
	endpoint := resolveOllamaEndpoint(c.endpoint)
	ctx, span := startLLMRequestSpan(ctx, "ollama", req.Model, endpoint, true)
	defer span.End()
	streamCtx, streamSpan := observability.TracerFor("internal/agent/llm_client").StartSpan(ctx, "llm.stream",
		observability.WithSpanKind(observability.SpanKindInternal),
	)
	defer streamSpan.End()

	wireReq := ollamaWireRequest{
		Model:    req.Model,
		Messages: toOllamaMessages(stripCacheControl(req.Messages)),
		Tools:    convertToolsToAny(req.Tools),
		Stream:   true,
		Options: map[string]any{
			"temperature": req.Temperature,
		},
	}

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
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := sharedLLMHTTPClient.Do(httpReq)
	if err != nil {
		span.RecordError(err, observability.StatusFromError(err))
		return nil, fmt.Errorf("请求 ollama 失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		recordCompletedStreamPayloads(ctx, span, body, buildStreamHTTPErrorPayload(resp.StatusCode, respBytes), "error",
			observability.Int("llm.response.status_code", resp.StatusCode),
		)
		err := fmt.Errorf("Ollama 流式请求失败，状态码: %d，响应: %s", resp.StatusCode, string(respBytes))
		span.RecordError(err, observability.SpanStatusError)
		return nil, err
	}

	reader := bufio.NewReader(resp.Body)
	var contentBuilder strings.Builder
	promptTokens := 0
	completionTokens := 0
	var toolCalls []types.MessageToolCall
	firstTokenEventSent := false
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			recordCompletedStreamPayloads(ctx, span, body, buildStreamReadErrorPayload(contentBuilder.String(), promptTokens, completionTokens, err), "error",
				observability.Int("llm.response.status_code", resp.StatusCode),
			)
			span.RecordError(err, observability.StatusFromError(err))
			return nil, fmt.Errorf("读取流式响应失败: %w", err)
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
			contentBuilder.WriteString(chunk.Message.Content)
			if !firstTokenEventSent {
				firstTokenEventSent = true
				streamSpan.AddEvent("first_token_received", observability.Int("token.length", len(chunk.Message.Content)))
			}
			onToken(chunk.Message.Content)
		}
		if len(chunk.Message.ToolCalls) > 0 {
			parsed, parseErr := messageToolCallsFromOllama(chunk.Message.ToolCalls)
			if parseErr != nil {
				span.RecordError(parseErr, observability.SpanStatusLLMInvalidResponse)
				return nil, parseErr
			}
			toolCalls = parsed
		}
		if chunk.PromptEvalCount > 0 {
			promptTokens = chunk.PromptEvalCount
		}
		if chunk.EvalCount > 0 {
			completionTokens = chunk.EvalCount
		}

		if chunk.Done {
			break
		}
	}

	streamSpan.SetAttributes(
		observability.Int("llm.usage.input_tokens", promptTokens),
		observability.Int("llm.usage.output_tokens", completionTokens),
	)
	streamSpan.AddEvent("stream.completed",
		observability.Int("llm.usage.input_tokens", promptTokens),
		observability.Int("llm.usage.output_tokens", completionTokens),
	)
	streamSpan.SetStatus(observability.SpanStatusOK, "llm stream completed")
	finalResp := &ChatResponse{
		Message:          buildAssistantMessage(string(types.RoleAssistant), contentBuilder.String(), toolCalls, ""),
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
	}
	recordCompletedStreamPayloads(ctx, span, body, buildStreamSuccessPayload(finalResp), "response",
		observability.Int("llm.response.status_code", resp.StatusCode),
	)
	span.SetAttributes(
		observability.Int("llm.usage.input_tokens", promptTokens),
		observability.Int("llm.usage.output_tokens", completionTokens),
	)
	span.SetStatus(observability.SpanStatusOK, "llm request completed")
	return finalResp, nil
}
