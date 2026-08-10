package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/config"
)

func TestNewChatModelCallsOpenAICompatibleAPI(t *testing.T) {
	var requestPath string
	var authorization string
	var requestBody map[string]any
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestPath = request.URL.Path
		authorization = request.Header.Get("Authorization")
		require.NoError(t, json.NewDecoder(request.Body).Decode(&requestBody))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"id":"chatcmpl-test",
				"object":"chat.completion",
				"created":1,
				"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"连接成功"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`)),
			Request: request,
		}, nil
	})}

	chatModel, err := NewChatModel(context.Background(), config.LLMConfig{
		Provider: "openai-compatible",
		Endpoint: "https://llm.example.com/v1",
		APIKey:   "test-key",
		Model:    "test-model",
	}, WithHTTPClient(client))
	require.NoError(t, err)
	chatModel, err = chatModel.WithTools([]*schema.ToolInfo{{
		Name: "query_metrics",
		Desc: "查询当前事件的指标",
	}})
	require.NoError(t, err)

	result, err := chatModel.Generate(context.Background(), []*schema.Message{schema.UserMessage("ping")})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "连接成功", result.Content)
	assert.Equal(t, "/v1/chat/completions", requestPath)
	assert.Equal(t, "Bearer test-key", authorization)
	assert.Equal(t, "test-model", requestBody["model"])
	assert.NotEmpty(t, requestBody["tools"])
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestNewChatModelValidatesConfig(t *testing.T) {
	tests := []struct {
		name      string
		config    config.LLMConfig
		wantError string
	}{
		{name: "missing provider", config: config.LLMConfig{Model: "test-model"}, wantError: "LLM provider is required"},
		{name: "missing model", config: config.LLMConfig{Provider: "openai"}, wantError: "LLM model is required"},
		{name: "missing compatible endpoint", config: config.LLMConfig{Provider: "openai-compatible", Model: "test-model"}, wantError: `LLM endpoint is required for provider "openai-compatible"`},
		{name: "unsupported provider", config: config.LLMConfig{Provider: "claude", Model: "test-model"}, wantError: `unsupported LLM provider "claude"`},
		{name: "invalid endpoint", config: config.LLMConfig{Provider: "openai", Model: "test-model", Endpoint: "localhost:8080"}, wantError: `invalid LLM endpoint "localhost:8080": must be an absolute HTTP(S) URL`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := NewChatModel(context.Background(), test.config)
			assert.Nil(t, result)
			require.EqualError(t, err, test.wantError)
		})
	}
}

func TestEndpointForProviderSupportsOllamaCompatibility(t *testing.T) {
	endpoint, err := endpointForProvider("ollama", "http://localhost:11434")
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:11434/v1", endpoint)

	endpoint, err = endpointForProvider("ollama", "http://localhost:11434/v1/")
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:11434/v1", endpoint)
}
