package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/config"
)

func TestThinkingAgentStepReturnsRunActionFromOllama(t *testing.T) {
	tmpDir := t.TempDir()

	for _, f := range []struct {
		name string
		body string
	}{
		{"system_prompt", "你是一个测试助手。"},
		{"user_prompt", "示例：这是少样本提示。"},
	} {
		path := filepath.Join(tmpDir, f.name+".md")
		if err := os.WriteFile(path, []byte(f.body), 0644); err != nil {
			t.Fatalf("write prompt file: %v", err)
		}
	}

	var capturedReq ollamaWireRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&capturedReq); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		resp := ollamaWireResponse{
			PromptEvalCount: 123,
			EvalCount:       45,
		}
		resp.Message.Role = "assistant"
		resp.Message.Content = `<execute_bash>ls -la</execute_bash>`
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	previous := config.Global
	config.Global = &config.Config{
		LLM: config.LLMConfig{
			Provider:   "ollama",
			Model:      "test-model",
			Endpoint:   server.URL,
			PromptsDir: tmpDir,
		},
	}
	t.Cleanup(func() {
		config.Global = previous
	})

	agent, err := NewBaseAgent(config.LLMConfig{
		Provider:   "ollama",
		Model:      "test-model",
		Endpoint:   server.URL,
		PromptsDir: tmpDir,
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	action, err := agent.Step(context.Background(), &types.State{
		History: []types.Event{
			&types.MessageAction{
				BaseEvent: types.BaseEvent{Source: types.SourceUser},
				Content:   "看看当前目录里有什么",
			},
		},
	})
	if err != nil {
		t.Fatalf("step returned error: %v", err)
	}

	if len(capturedReq.Messages) < 3 {
		t.Fatalf("expected system + example + user message, got %d messages", len(capturedReq.Messages))
	}

	runAction, ok := action.(*types.CmdRunAction)
	if !ok {
		t.Fatalf("expected CmdRunAction, got %T", action)
	}
	if runAction.Command != "ls -la" {
		t.Fatalf("unexpected command %q", runAction.Command)
	}
	if runAction.GetBase().LLMMetrics == nil {
		t.Fatal("expected LLM metrics to be attached")
	}
	if runAction.GetBase().LLMMetrics.PromptTokens != 123 || runAction.GetBase().LLMMetrics.CompletionTokens != 45 {
		t.Fatalf("unexpected metrics %+v", runAction.GetBase().LLMMetrics)
	}
}

func TestThinkingAgentStepReturnsFinishActionFromOpenAICompatibleLive(t *testing.T) {
	if os.Getenv("RUN_LIVE_LLM_TESTS") != "1" {
		t.Skip("set RUN_LIVE_LLM_TESTS=1 to run live openai-compatible test")
	}

	config.Load()
	previous := config.Global
	config.Global = &config.Config{
		LLM: config.LLMConfig{
			Provider: "openai",
			Model:    "qwen3.5-plus",
			Endpoint: "https://dashscope.aliyuncs.com/compatible-mode/v1",
			APIKey:   previous.LLM.APIKey,
		},
	}
	t.Cleanup(func() {
		config.Global = previous
	})

	agent, err := NewBaseAgent(config.LLMConfig{
		Provider: "openai",
		Model:    "qwen3.5-plus",
		Endpoint: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		APIKey:   previous.LLM.APIKey,
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	action, err := agent.Step(context.Background(), &types.State{
		History: []types.Event{
			&types.MessageAction{
				BaseEvent: types.BaseEvent{Source: types.SourceUser},
				Content:   "请直接结束并返回任务已完成",
			},
		},
	})
	if err != nil {
		t.Fatalf("step returned error: %v", err)
	}
	if _, ok := action.(*types.FinishAction); !ok {
		t.Fatalf("expected FinishAction, got %T", action)
	}
}

func TestThinkingAgentStepReturnsRunActionFromOpenAICompatibleLive(t *testing.T) {
	if os.Getenv("RUN_LIVE_LLM_TESTS") != "1" {
		t.Skip("set RUN_LIVE_LLM_TESTS=1 to run live openai-compatible test")
	}

	config.Load()
	previous := config.Global
	config.Global = &config.Config{
		LLM: config.LLMConfig{
			Provider: "openai",
			Model:    "qwen3.5-plus",
			Endpoint: "https://dashscope.aliyuncs.com/compatible-mode/v1",
			APIKey:   previous.LLM.APIKey,
		},
	}
	t.Cleanup(func() {
		config.Global = previous
	})

	agent, err := NewBaseAgent(config.LLMConfig{
		Provider: "openai",
		Model:    "qwen3.5-plus",
		Endpoint: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		APIKey:   previous.LLM.APIKey,
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	action, err := agent.Step(context.Background(), &types.State{
		History: []types.Event{
			&types.MessageAction{
				BaseEvent: types.BaseEvent{Source: types.SourceUser},
				Content:   "看看当前目录里有什么",
			},
		},
	})
	if err != nil {
		t.Fatalf("step returned error: %v", err)
	}
	if _, ok := action.(*types.CmdRunAction); !ok {
		t.Fatalf("expected CmdRunAction, got %T", action)
	}
}
