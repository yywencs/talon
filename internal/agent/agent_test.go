package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/config"
)

func TestThinkingAgentStepReturnsRunActionFromOllama(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}

		var req ollamaWireRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "test-model" {
			t.Fatalf("unexpected model %q", req.Model)
		}
		if len(req.Messages) < 2 {
			t.Fatalf("expected prompt and user message, got %d messages", len(req.Messages))
		}

		resp := ollamaWireResponse{
			PromptEvalCount: 123,
			EvalCount:       45,
		}
		resp.Message.Role = "assistant"
		resp.Message.Content = `{"action":"run","command":"ls -la","thought":"先看看目录"}`

		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer server.Close()

	previous := config.Global
	config.Global = &config.Config{
		LLM: config.LLMConfig{
			Provider: "ollama",
			Model:    "test-model",
			Endpoint: server.URL,
		},
	}
	t.Cleanup(func() {
		config.Global = previous
	})

	agent, err := NewThinkingAgent(config.LLMConfig{
		Provider: "ollama",
		Model:    "test-model",
		Endpoint: server.URL,
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
	t.Logf("LLM Config: %v", config.Global.LLM)
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
	t.Logf("LLM API Key: %s", config.Global.LLM.APIKey)

	agent, err := NewThinkingAgent(config.LLMConfig{
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

	agent, err := NewThinkingAgent(config.LLMConfig{
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
