package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/config"
	"github.com/wen/opentalon/pkg/observability"
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
	resp, err := client.StreamChat(ctx, req, func(token string) {
		fmt.Print(token)
		fullResponse += token
	})

	if err != nil {
		t.Fatalf("流式请求失败: %v", err)
	}

	if len(fullResponse) == 0 {
		t.Error("未收到任何 token")
	}
	if resp == nil {
		t.Fatal("expected final response")
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
	resp, err := client.StreamChat(ctx, req, func(token string) {
		fmt.Print(token)
		fullResponse += token
	})

	if err != nil {
		t.Fatalf("流式请求失败: %v", err)
	}

	if len(fullResponse) == 0 {
		t.Error("未收到任何 token")
	}
	if resp == nil {
		t.Fatal("expected final response")
	}

	t.Logf("\n总共收到 %d 个字符", len(fullResponse))
}

func TestOpenAICompatibleStreamChatCapturesPayloadAfterCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer does not support flushing")
		}
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"你好\"}}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1}}\n\n"))
		flusher.Flush()
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"世界\"}}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	traceRoot := t.TempDir()
	cfg := observability.DefaultConfig()
	cfg.Enabled = true
	cfg.Exporters = []observability.ExporterKind{observability.ExporterJSONL}
	cfg.TraceDir = traceRoot
	if err := observability.Init(context.Background(), cfg); err != nil {
		t.Fatalf("observability.Init() error = %v", err)
	}
	t.Cleanup(func() {
		_ = observability.Shutdown(context.Background())
	})

	client := newOpenAIClient(server.URL, "stream-secret")
	var duringStreamChecked bool
	resp, err := client.StreamChat(context.Background(), ChatRequest{
		Model: "test-model",
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.Content{types.TextContent{Text: "你好"}}},
		},
		Temperature: 0,
	}, func(token string) {
		if token == "" || duringStreamChecked {
			return
		}
		duringStreamChecked = true
		if got := len(findFilesWithSuffix(t, traceRoot, "-request-payload.json")); got != 0 {
			t.Fatalf("request payload files during stream = %d, want 0", got)
		}
		if got := len(findFilesWithSuffix(t, traceRoot, "-response-payload.json")); got != 0 {
			t.Fatalf("response payload files during stream = %d, want 0", got)
		}
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	if !duringStreamChecked {
		t.Fatal("duringStreamChecked = false, want true")
	}
	if utils.FlattenTextContent(resp.Message.Content) != "你好世界" {
		t.Fatalf("response content = %q, want 你好世界", utils.FlattenTextContent(resp.Message.Content))
	}

	if shutdownErr := observability.Shutdown(context.Background()); shutdownErr != nil {
		t.Fatalf("observability.Shutdown() error = %v", shutdownErr)
	}

	requestFiles := findFilesWithSuffix(t, traceRoot, "-request-payload.json")
	responseFiles := findFilesWithSuffix(t, traceRoot, "-response-payload.json")
	if len(requestFiles) != 1 {
		t.Fatalf("request payload file count = %d, want 1", len(requestFiles))
	}
	if len(responseFiles) != 1 {
		t.Fatalf("response payload file count = %d, want 1", len(responseFiles))
	}
	requestContent, err := os.ReadFile(requestFiles[0])
	if err != nil {
		t.Fatalf("ReadFile(request) error = %v", err)
	}
	if strings.Contains(string(requestContent), "stream-secret") {
		t.Fatalf("request payload leaked secret: %s", string(requestContent))
	}
	spanRecords := readSpanRecords(t, traceRoot)
	attrs := spanRecords[len(spanRecords)-1]["attributes"].(map[string]any)
	if attrs["llm.request.artifact_path"] == "" || attrs["llm.response.artifact_path"] == "" {
		t.Fatalf("artifact paths missing: %+v", attrs)
	}
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

func TestOllamaStreamChatCapturesPayloadAfterCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer does not support flushing")
		}
		_, _ = w.Write([]byte("{\"message\":{\"role\":\"assistant\",\"content\":\"你好\"},\"done\":false}\n"))
		flusher.Flush()
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte("{\"message\":{\"role\":\"assistant\",\"content\":\"世界\"},\"done\":true,\"prompt_eval_count\":3,\"eval_count\":2}\n"))
		flusher.Flush()
	}))
	defer server.Close()

	traceRoot := t.TempDir()
	cfg := observability.DefaultConfig()
	cfg.Enabled = true
	cfg.Exporters = []observability.ExporterKind{observability.ExporterJSONL}
	cfg.TraceDir = traceRoot
	if err := observability.Init(context.Background(), cfg); err != nil {
		t.Fatalf("observability.Init() error = %v", err)
	}
	t.Cleanup(func() {
		_ = observability.Shutdown(context.Background())
	})

	client := newOllamaClient(server.URL)
	var duringStreamChecked bool
	resp, err := client.StreamChat(context.Background(), ChatRequest{
		Model: "ollama-test",
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.Content{types.TextContent{Text: "你好"}}},
		},
		Temperature: 0,
	}, func(token string) {
		if token == "" || duringStreamChecked {
			return
		}
		duringStreamChecked = true
		if got := len(findFilesWithSuffix(t, traceRoot, "-request-payload.json")); got != 0 {
			t.Fatalf("request payload files during stream = %d, want 0", got)
		}
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	if utils.FlattenTextContent(resp.Message.Content) != "你好世界" {
		t.Fatalf("response content = %q, want 你好世界", utils.FlattenTextContent(resp.Message.Content))
	}
	if err := observability.Shutdown(context.Background()); err != nil {
		t.Fatalf("observability.Shutdown() error = %v", err)
	}

	requestFiles := findFilesWithSuffix(t, traceRoot, "-request-payload.json")
	responseFiles := findFilesWithSuffix(t, traceRoot, "-response-payload.json")
	if len(requestFiles) != 1 {
		t.Fatalf("request payload file count = %d, want 1", len(requestFiles))
	}
	if len(responseFiles) != 1 {
		t.Fatalf("response payload file count = %d, want 1", len(responseFiles))
	}
	spanRecords := readSpanRecords(t, traceRoot)
	attrs := spanRecords[len(spanRecords)-1]["attributes"].(map[string]any)
	if attrs["llm.response.artifact_path"] == "" {
		t.Fatalf("llm.response.artifact_path is empty: %+v", attrs)
	}
	if got := fmt.Sprint(attrs["llm.response.status_code"]); got != "200" {
		t.Fatalf("llm.response.status_code = %v, want 200", attrs["llm.response.status_code"])
	}
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

func TestDoJSONRequestCapturesPayloadArtifactsAndSummary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message":       "ok",
			"authorization": "Bearer response-secret",
		})
	}))
	defer server.Close()

	traceRoot := t.TempDir()
	cfg := observability.DefaultConfig()
	cfg.Enabled = true
	cfg.Exporters = []observability.ExporterKind{observability.ExporterJSONL}
	cfg.TraceDir = traceRoot
	if err := observability.Init(context.Background(), cfg); err != nil {
		t.Fatalf("observability.Init() error = %v", err)
	}
	t.Cleanup(func() {
		_ = observability.Shutdown(context.Background())
	})

	ctx, span := observability.StartSpan(context.Background(), "llm.request.test")
	reqBody := map[string]any{
		"message": "hello",
		"api_key": "request-secret",
	}
	var respBody map[string]any
	if err := doJSONRequest(ctx, sharedLLMHTTPClient, server.URL, reqBody, map[string]string{
		"Authorization": "Bearer header-secret",
	}, &respBody); err != nil {
		t.Fatalf("doJSONRequest() error = %v", err)
	}
	span.End()
	if err := observability.Shutdown(context.Background()); err != nil {
		t.Fatalf("observability.Shutdown() error = %v", err)
	}

	requestFiles := findFilesWithSuffix(t, traceRoot, "-request-payload.json")
	responseFiles := findFilesWithSuffix(t, traceRoot, "-response-payload.json")
	if len(requestFiles) != 1 {
		t.Fatalf("request payload file count = %d, want 1", len(requestFiles))
	}
	if len(responseFiles) != 1 {
		t.Fatalf("response payload file count = %d, want 1", len(responseFiles))
	}

	requestContent, err := os.ReadFile(requestFiles[0])
	if err != nil {
		t.Fatalf("ReadFile(request) error = %v", err)
	}
	if strings.Contains(string(requestContent), "request-secret") {
		t.Fatalf("request payload leaked secret: %s", string(requestContent))
	}
	responseContent, err := os.ReadFile(responseFiles[0])
	if err != nil {
		t.Fatalf("ReadFile(response) error = %v", err)
	}
	if strings.Contains(string(responseContent), "response-secret") {
		t.Fatalf("response payload leaked secret: %s", string(responseContent))
	}

	spanRecords := readSpanRecords(t, traceRoot)
	if len(spanRecords) == 0 {
		t.Fatal("no span records found")
	}
	attrs := spanRecords[len(spanRecords)-1]["attributes"].(map[string]any)
	if attrs["llm.request.artifact_path"] == "" {
		t.Fatalf("llm.request.artifact_path is empty: %+v", attrs)
	}
	if attrs["llm.response.artifact_path"] == "" {
		t.Fatalf("llm.response.artifact_path is empty: %+v", attrs)
	}
	if attrs["llm.request.body_size"] == nil {
		t.Fatalf("llm.request.body_size is missing: %+v", attrs)
	}
	if attrs["llm.response.body_size"] == nil {
		t.Fatalf("llm.response.body_size is missing: %+v", attrs)
	}
	if strings.Contains(fmt.Sprint(attrs["llm.request.preview"]), "request-secret") {
		t.Fatalf("request preview leaked secret: %+v", attrs["llm.request.preview"])
	}
	if strings.Contains(fmt.Sprint(attrs["llm.response.preview"]), "response-secret") {
		t.Fatalf("response preview leaked secret: %+v", attrs["llm.response.preview"])
	}
}

func TestDoJSONRequestCapturesFailureResponsePayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":         "bad request",
			"authorization": "Bearer failure-secret",
		})
	}))
	defer server.Close()

	traceRoot := t.TempDir()
	cfg := observability.DefaultConfig()
	cfg.Enabled = true
	cfg.Exporters = []observability.ExporterKind{observability.ExporterJSONL}
	cfg.TraceDir = traceRoot
	if err := observability.Init(context.Background(), cfg); err != nil {
		t.Fatalf("observability.Init() error = %v", err)
	}
	t.Cleanup(func() {
		_ = observability.Shutdown(context.Background())
	})

	ctx, span := observability.StartSpan(context.Background(), "llm.request.failure")
	err := doJSONRequest(ctx, sharedLLMHTTPClient, server.URL, map[string]any{
		"message": "hello",
	}, nil, &map[string]any{})
	if err == nil {
		t.Fatal("doJSONRequest() error = nil, want non-nil")
	}
	span.End()
	if err := observability.Shutdown(context.Background()); err != nil {
		t.Fatalf("observability.Shutdown() error = %v", err)
	}

	responseFiles := findFilesWithSuffix(t, traceRoot, "-response-payload.json")
	if len(responseFiles) != 1 {
		t.Fatalf("response payload file count = %d, want 1", len(responseFiles))
	}
	responseContent, readErr := os.ReadFile(responseFiles[0])
	if readErr != nil {
		t.Fatalf("ReadFile(response) error = %v", readErr)
	}
	if strings.Contains(string(responseContent), "failure-secret") {
		t.Fatalf("failure response payload leaked secret: %s", string(responseContent))
	}

	spanRecords := readSpanRecords(t, traceRoot)
	if len(spanRecords) == 0 {
		t.Fatal("no span records found")
	}
	attrs := spanRecords[len(spanRecords)-1]["attributes"].(map[string]any)
	if got := fmt.Sprint(attrs["llm.response.status_code"]); got != "400" {
		t.Fatalf("llm.response.status_code = %v, want 400", attrs["llm.response.status_code"])
	}
	if attrs["llm.response.artifact_path"] == "" {
		t.Fatalf("llm.response.artifact_path is empty: %+v", attrs)
	}
}

func findFilesWithSuffix(t *testing.T, root, suffix string) []string {
	t.Helper()
	matches := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, suffix) {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir() error = %v", err)
	}
	return matches
}

func readSpanRecords(t *testing.T, root string) []map[string]any {
	t.Helper()
	spanFiles := findFilesWithSuffix(t, root, "spans.jsonl")
	if len(spanFiles) != 1 {
		t.Fatalf("spans.jsonl count = %d, want 1", len(spanFiles))
	}
	raw, err := os.ReadFile(spanFiles[0])
	if err != nil {
		t.Fatalf("ReadFile(spans.jsonl) error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	out := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		record := make(map[string]any)
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("json.Unmarshal(span line) error = %v", err)
		}
		out = append(out, record)
	}
	return out
}
