package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/config"
	"github.com/wen/opentalon/pkg/utils"
)

func TestOllamaStreamChat(t *testing.T) {
	config.Load()
	cfg := config.Global

	if cfg.LLM.Provider != "ollama" {
		t.Skip("skipping ollama streaming test, provider is not ollama")
	}

	client, err := NewLLMClient(cfg.LLM)
	if err != nil {
		t.Fatalf("创建 LLM 客户端失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	req := ChatRequest{
		Model: cfg.LLM.Model,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.Content{types.TextContent{Text: "请用一句话介绍 Go 语言的特色"}}},
		},
		Temperature: 0.7,
	}

	var fullResponse string
	err = client.StreamChat(ctx, req, func(token string) {
		fmt.Print(token)
		fullResponse += token
	})

	if err != nil {
		t.Fatalf("流式请求失败: %v", err)
	}

	if len(fullResponse) == 0 {
		t.Error("未收到任何 token")
	}

	t.Logf("\n总共收到 %d 个字符", len(fullResponse))
}

func TestOpenAICompatibleStreamChat(t *testing.T) {
	if os.Getenv("RUN_LIVE_LLM_TESTS") != "1" {
		t.Skip("set RUN_LIVE_LLM_TESTS=1 to run live openai-compatible streaming test")
	}
	config.Load()
	cfg := config.Global

	if cfg.LLM.Provider == "ollama" {
		t.Skip("skipping openai-compatible streaming test, provider is ollama")
	}

	client, err := NewLLMClient(cfg.LLM)
	if err != nil {
		t.Fatalf("创建 LLM 客户端失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	req := ChatRequest{
		Model: cfg.LLM.Model,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.Content{types.TextContent{Text: "请用一句话介绍 Go 语言的特色"}}},
		},
		Temperature: 0.7,
	}

	var fullResponse string
	err = client.StreamChat(ctx, req, func(token string) {
		fmt.Print(token)
		fullResponse += token
	})

	if err != nil {
		t.Fatalf("流式请求失败: %v", err)
	}

	if len(fullResponse) == 0 {
		t.Error("未收到任何 token")
	}

	t.Logf("\n总共收到 %d 个字符", len(fullResponse))
}

func TestOllamaChat(t *testing.T) {
	config.Load()
	cfg := config.Global

	if cfg.LLM.Provider != "ollama" {
		t.Skip("skipping ollama chat test, provider is not ollama")
	}

	client, err := NewLLMClient(cfg.LLM)
	if err != nil {
		t.Fatalf("创建 LLM 客户端失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	req := ChatRequest{
		Model: cfg.LLM.Model,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.Content{types.TextContent{Text: "1+1等于几？"}}},
		},
		Temperature: 0,
	}

	resp, err := client.Chat(ctx, req)
	if err != nil {
		t.Fatalf("Chat 请求失败: %v", err)
	}

	if len(resp.Message.Content) == 0 {
		t.Error("未收到任何内容")
	}

	t.Logf("响应内容: %s", utils.FlattenTextContent(resp.Message.Content))
	t.Logf("Prompt tokens: %d, Completion tokens: %d", resp.PromptTokens, resp.CompletionTokens)
}

func TestOpenAICompatibleChat(t *testing.T) {
	config.Load()
	cfg := config.Global

	if cfg.LLM.Provider == "ollama" {
		t.Skip("skipping openai-compatible chat test, provider is ollama")
	}

	client, err := NewLLMClient(cfg.LLM)
	if err != nil {
		t.Fatalf("创建 LLM 客户端失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	req := ChatRequest{
		Model: cfg.LLM.Model,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.Content{types.TextContent{Text: "1+1等于几？"}}},
		},
		Temperature: 0,
	}

	resp, err := client.Chat(ctx, req)
	if err != nil {
		t.Fatalf("Chat 请求失败: %v", err)
	}

	if len(resp.Message.Content) == 0 {
		t.Error("未收到任何内容")
	}

	t.Logf("响应内容: %s", utils.FlattenTextContent(resp.Message.Content))
	t.Logf("Prompt tokens: %d, Completion tokens: %d", resp.PromptTokens, resp.CompletionTokens)
}

func TestNewLLMClientFactory(t *testing.T) {
	config.Load()
	cfg := config.Global

	client, err := NewLLMClient(cfg.LLM)
	if err != nil {
		t.Fatalf("创建 LLM 客户端失败: %v", err)
	}

	if client == nil {
		t.Error("客户端不应为 nil")
	}

	_, ok := client.(LLMClient)
	if !ok {
		t.Error("客户端应实现 LLMClient 接口")
	}
}

func TestStripCacheControl(t *testing.T) {
	messages := []types.Message{
		{
			Role: types.RoleSystem,
			Content: []types.Content{
				types.TextContent{Text: "system prompt", BaseContent: types.BaseContent{CachePrompt: true}},
			},
		},
		{
			Role: types.RoleUser,
			Content: []types.Content{
				types.TextContent{Text: "hello"},
			},
		},
	}

	sanitized := stripCacheControl(messages)

	if len(sanitized[0].Content) == 0 {
		t.Fatal("expected content to remain")
	}
	if tc, ok := sanitized[0].Content[0].(types.TextContent); !ok || tc.CachePrompt {
		t.Fatalf("expected cache_prompt to be stripped, got %+v", sanitized[0].Content[0])
	}
	if tc, ok := messages[0].Content[0].(types.TextContent); !ok || !tc.CachePrompt {
		t.Fatal("stripCacheControl should not mutate original messages")
	}
}

func TestSerializeOpenAIChatMessagesWithCacheControl(t *testing.T) {
	messages, err := serializeOpenAIChatMessages([]types.Message{
		{
			Role: types.RoleSystem,
			Content: []types.Content{
				types.TextContent{Text: "system prompt", BaseContent: types.BaseContent{CachePrompt: true}},
			},
		},
	})
	if err != nil {
		t.Fatalf("serialize messages failed: %v", err)
	}

	wireReq := openAIWireRequest{
		Model:    "gpt-test",
		Messages: messages,
	}
	data, err := json.Marshal(wireReq)
	if err != nil {
		t.Fatalf("marshal wire request failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal payload failed: %v", err)
	}

	wireMessages, ok := payload["messages"].([]any)
	if !ok || len(wireMessages) != 1 {
		t.Fatalf("messages should be a single item array: %s", string(data))
	}

	messagePayload, ok := wireMessages[0].(map[string]any)
	if !ok {
		t.Fatalf("message payload should be an object: %s", string(data))
	}
	if _, exists := messagePayload["cache_control"]; exists {
		t.Fatalf("cache_control should not appear on message top level: %s", string(data))
	}

	content, ok := messagePayload["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content should be a single text block array: %s", string(data))
	}

	block, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content block should be an object: %s", string(data))
	}
	if block["type"] != "text" {
		t.Fatalf("unexpected content block type: %s", string(data))
	}
	if block["text"] != "system prompt" {
		t.Fatalf("unexpected content block text: %s", string(data))
	}

	cacheControl, ok := block["cache_control"].(map[string]any)
	if !ok || cacheControl["type"] != "ephemeral" {
		t.Fatalf("cache_control should appear inside content block: %s", string(data))
	}
}

func TestMessageFromOpenAIChoice(t *testing.T) {
	msg, err := messageFromOpenAIChoice(openAIWireMessage{
		Role:             "assistant",
		Content:          "这里是说明",
		ReasoningContent: "这是推理",
		ToolCalls: []types.ChatToolCallInput{
			{
				ID:   "call_1",
				Type: "function",
				Function: &types.ChatToolCallFunction{
					Name:      "bash",
					Arguments: `{"command":"pwd"}`,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("messageFromOpenAIChoice failed: %v", err)
	}
	if msg.Role != types.RoleAssistant {
		t.Fatalf("expected assistant role, got %q", msg.Role)
	}
	if utils.FlattenTextContent(msg.Content) != "这里是说明" {
		t.Fatalf("unexpected content: %+v", msg.Content)
	}
	if msg.ReasoningContent != "这是推理" {
		t.Fatalf("unexpected reasoning_content: %q", msg.ReasoningContent)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].Name != "bash" || msg.ToolCalls[0].Arguments != `{"command":"pwd"}` {
		t.Fatalf("unexpected tool call: %+v", msg.ToolCalls[0])
	}
}
