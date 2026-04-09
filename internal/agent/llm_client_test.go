package agent

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/wen/opentalon/pkg/config"
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
		Messages: []ChatMessage{
			{Role: "user", Content: "请用一句话介绍 Go 语言的特色"},
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
		Messages: []ChatMessage{
			{Role: "user", Content: "请用一句话介绍 Go 语言的特色"},
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
		Messages: []ChatMessage{
			{Role: "user", Content: "1+1等于几？"},
		},
		Temperature: 0,
	}

	resp, err := client.Chat(ctx, req)
	if err != nil {
		t.Fatalf("Chat 请求失败: %v", err)
	}

	if resp.Content == "" {
		t.Error("未收到任何内容")
	}

	t.Logf("响应内容: %s", resp.Content)
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
		Messages: []ChatMessage{
			{Role: "user", Content: "1+1等于几？"},
		},
		Temperature: 0,
	}

	resp, err := client.Chat(ctx, req)
	if err != nil {
		t.Fatalf("Chat 请求失败: %v", err)
	}

	if resp.Content == "" {
		t.Error("未收到任何内容")
	}

	t.Logf("响应内容: %s", resp.Content)
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
