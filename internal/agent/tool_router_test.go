package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/wen/opentalon/internal/tool"
	"github.com/wen/opentalon/internal/types"
)

func TestToolRouter_ParseLLMResponse_SingleBashTool(t *testing.T) {
	router := NewToolRouter()
	factory, ok := tool.Get("bash")
	if !ok {
		t.Fatal("bash tool not registered")
	}
	router.Register("bash", factory)

	resp := &ChatResponse{
		Message: types.Message{
			Role:    types.RoleAssistant,
			Content: []types.Content{types.TextContent{Text: "这里执行一个命令"}},
			ToolCalls: []types.MessageToolCall{
				{ID: "call_001", Name: "bash", Arguments: `{"command":"echo hello","summary":"test","security_risk":"HIGH"}`},
			},
		},
	}

	calls, plainText, err := router.ParseLLMResponse(resp)
	if err != nil {
		t.Fatalf("ParseLLMResponse failed: %v", err)
	}
	if plainText != "这里执行一个命令" {
		t.Fatalf("expected plain text, got %q", plainText)
	}
	if len(calls) != 1 || calls[0].Name != "bash" {
		t.Fatalf("expected 1 bash call, got %d", len(calls))
	}
}

func TestToolRouter_ParseLLMResponse_MultipleTools(t *testing.T) {
	router := NewToolRouter()
	factory, ok := tool.Get("bash")
	if !ok {
		t.Fatal("bash tool not registered")
	}
	router.Register("bash", factory)

	resp := &ChatResponse{
		Message: types.Message{
			Role: types.RoleAssistant,
			ToolCalls: []types.MessageToolCall{
				{ID: "call_001", Name: "bash", Arguments: `{"command":"echo 1","summary":"test","security_risk":"HIGH"}`},
				{ID: "call_002", Name: "bash", Arguments: `{"command":"echo 2","summary":"test","security_risk":"HIGH"}`},
			},
		},
	}

	calls, _, err := router.ParseLLMResponse(resp)
	if err != nil {
		t.Fatalf("ParseLLMResponse failed: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
}

func TestToolRouter_PlainMessage(t *testing.T) {
	router := NewToolRouter()

	resp := &ChatResponse{
		Message: types.Message{
			Role:    types.RoleAssistant,
			Content: []types.Content{types.TextContent{Text: "这是一条普通消息"}},
		},
	}

	calls, plainText, err := router.ParseLLMResponse(resp)
	if err != nil {
		t.Fatalf("ParseLLMResponse failed: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("expected 0 calls, got %d", len(calls))
	}
	if plainText != "这是一条普通消息" {
		t.Fatalf("expected plain message, got: %s", plainText)
	}
}

func TestToolRouter_BuildActionEvents_UnknownTool(t *testing.T) {
	router := NewToolRouter()
	_, err := router.BuildActionEvents(context.Background(), []ToolCall{
		{
			ID:        "call_001",
			Name:      "unknown_tool",
			Arguments: json.RawMessage(`{}`),
		},
	})
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

func TestToolRouter_EmptyToolCalls(t *testing.T) {
	router := NewToolRouter()

	resp := &ChatResponse{Message: types.Message{Role: types.RoleAssistant, ToolCalls: []types.MessageToolCall{}}}

	calls, plainText, err := router.ParseLLMResponse(resp)
	if err != nil {
		t.Fatalf("ParseLLMResponse failed: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("expected 0 calls, got %d", len(calls))
	}
	if plainText != "" {
		t.Fatalf("expected empty plain text, got %q", plainText)
	}
}

func TestToolRouter_ResolveTool(t *testing.T) {
	router := NewToolRouter()
	factory, ok := tool.Get("bash")
	if !ok {
		t.Fatal("bash tool not registered")
	}
	router.Register("bash", factory)

	resolved, err := router.ResolveTool(context.Background(), "bash")
	if err != nil {
		t.Fatalf("ResolveTool failed: %v", err)
	}
	if resolved == nil || resolved.Name() != "bash" {
		t.Fatalf("expected bash tool, got %+v", resolved)
	}
}

func TestToolRouter_BuildActionEvents(t *testing.T) {
	router := NewToolRouter()
	factory, ok := tool.Get("bash")
	if !ok {
		t.Fatal("bash tool not registered")
	}
	router.Register("bash", factory)

	actionEvents, err := router.BuildActionEvents(context.Background(), []ToolCall{
		{
			ID:        "call_001",
			Name:      "bash",
			Arguments: json.RawMessage(`{"command":"echo hello","summary":"test","security_risk":"HIGH"}`),
		},
	})
	if err != nil {
		t.Fatalf("BuildActionEvents failed: %v", err)
	}
	if len(actionEvents) != 1 {
		t.Fatalf("expected 1 action event, got %d", len(actionEvents))
	}
	if actionEvents[0].ActionID == "" {
		t.Fatal("expected action id to be set")
	}
	if actionEvents[0].ToolCall == nil || actionEvents[0].ToolCall.ID != "call_001" {
		t.Fatalf("unexpected tool call metadata: %+v", actionEvents[0].ToolCall)
	}

	action, ok := actionEvents[0].Action.(*tool.BashAction)
	if !ok {
		t.Fatalf("expected bash action, got %T", actionEvents[0].Action)
	}
	if action.Command != "echo hello" {
		t.Fatalf("unexpected action command: %q", action.Command)
	}
}

func TestToolRouter_ConcurrentSafety(t *testing.T) {
	factory, ok := tool.Get("bash")
	if !ok {
		t.Fatal("bash tool not registered")
	}

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := NewToolRouter()
			r.Register("bash", factory)
			_, err := r.BuildActionEvents(context.Background(), []ToolCall{
				{
					ID:        "call_001",
					Name:      "bash",
					Arguments: json.RawMessage(`{"command":"echo ok","summary":"test","security_risk":"HIGH"}`),
				},
			})
			if err != nil {
				t.Errorf("concurrent BuildActionEvents failed: %v", err)
			}
		}()
	}
	wg.Wait()
}
