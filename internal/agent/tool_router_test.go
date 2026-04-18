package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/wen/opentalon/internal/tool"
	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/utils"
)

func TestToolRouter_SingleBashTool(t *testing.T) {
	router := NewToolRouter()
	factory, ok := tool.Get("bash")
	if !ok {
		t.Fatal("bash tool not registered")
	}
	router.Register("bash", factory)

	resp := &ChatResponse{
		Content: "这里执行一个命令",
		ToolCalls: []types.MessageToolCall{
			{ID: "call_001", Name: "bash", Arguments: `{"command":"echo hello","ActionMetadata":{"Summary":"test","SecurityRisk":"HIGH"}}`},
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

	results := router.ExecuteTools(context.Background(), calls)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].IsError() {
		t.Fatalf("expected success, got error: %s", utils.FlattenTextContent(results[0].GetContent()))
	}
	if !contains(utils.FlattenTextContent(results[0].GetContent()), "hello") {
		t.Fatalf("expected output to contain 'hello', got: %s", utils.FlattenTextContent(results[0].GetContent()))
	}
}

func TestToolRouter_MultipleTools(t *testing.T) {
	router := NewToolRouter()
	factory, ok := tool.Get("bash")
	if !ok {
		t.Fatal("bash tool not registered")
	}
	router.Register("bash", factory)

	resp := &ChatResponse{
		Content: "",
		ToolCalls: []types.MessageToolCall{
			{ID: "call_001", Name: "bash", Arguments: `{"command":"echo 1","ActionMetadata":{"Summary":"test","SecurityRisk":"HIGH"}}`},
			{ID: "call_002", Name: "bash", Arguments: `{"command":"echo 2","ActionMetadata":{"Summary":"test","SecurityRisk":"HIGH"}}`},
		},
	}

	calls, _, err := router.ParseLLMResponse(resp)
	if err != nil {
		t.Fatalf("ParseLLMResponse failed: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}

	results := router.ExecuteTools(context.Background(), calls)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for _, obs := range results {
		if obs.IsError() {
			t.Fatalf("expected success, got error: %s", utils.FlattenTextContent(obs.GetContent()))
		}
	}
}

func TestToolRouter_PlainMessage(t *testing.T) {
	router := NewToolRouter()

	resp := &ChatResponse{Content: "这是一条普通消息", ToolCalls: nil}

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

func TestToolRouter_UnknownTool(t *testing.T) {
	router := NewToolRouter()

	resp := &ChatResponse{
		Content: "",
		ToolCalls: []types.MessageToolCall{
			{ID: "call_001", Name: "unknown_tool", Arguments: `{}`},
		},
	}

	calls, _, err := router.ParseLLMResponse(resp)
	if err != nil {
		t.Fatalf("ParseLLMResponse failed: %v", err)
	}

	results := router.ExecuteTools(context.Background(), calls)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].IsError() {
		t.Fatal("expected error for unknown tool")
	}
	if !contains(utils.FlattenTextContent(results[0].GetContent()), "unknown tool") {
		t.Fatalf("expected 'unknown tool' error, got: %s", utils.FlattenTextContent(results[0].GetContent()))
	}
}

func TestToolRouter_EmptyToolCalls(t *testing.T) {
	router := NewToolRouter()

	resp := &ChatResponse{Content: "", ToolCalls: []types.MessageToolCall{}}

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

func TestToolRouter_ConcurrentLimit(t *testing.T) {
	router := NewToolRouter().WithMaxParallelism(2)
	factory, ok := tool.Get("bash")
	if !ok {
		t.Fatal("bash tool not registered")
	}
	router.Register("bash", factory)

	var calls []ToolCall
	for i := 1; i <= 5; i++ {
		calls = append(calls, ToolCall{
			ID:   fmt.Sprintf("call_%d", i),
			Name: "bash",
			Arguments: json.RawMessage(fmt.Sprintf(
				`{"command":"echo %d","ActionMetadata":{"Summary":"test","SecurityRisk":"HIGH"}}`, i,
			)),
		})
	}

	results := router.ExecuteTools(context.Background(), calls)
	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}
	for _, obs := range results {
		if obs.IsError() {
			t.Fatalf("expected success, got error: %s", utils.FlattenTextContent(obs.GetContent()))
		}
	}
}

func TestToolRouter_EmptyCalls(t *testing.T) {
	router := NewToolRouter()
	results := router.ExecuteTools(context.Background(), nil)
	if results != nil {
		t.Fatalf("expected nil for empty calls, got %d results", len(results))
	}
}

func TestToolRouter_ConcurrentSafety(t *testing.T) {
	factory, ok := tool.Get("bash")
	if !ok {
		t.Fatal("bash tool not registered")
	}

	var wg sync.WaitGroup
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := NewToolRouter()
			r.Register("bash", factory)
			calls, _, _ := r.ParseLLMResponse(&ChatResponse{
				Content: "",
				ToolCalls: []types.MessageToolCall{
					{ID: "call_001", Name: "bash", Arguments: `{"command":"echo ok","ActionMetadata":{"Summary":"test","SecurityRisk":"HIGH"}}`},
				},
			})
			results := r.ExecuteTools(context.Background(), calls)
			for _, obs := range results {
				if obs.IsError() {
					t.Errorf("concurrent execution error: %s", utils.FlattenTextContent(obs.GetContent()))
				}
			}
		}()
	}
	wg.Wait()
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr))
}

func containsAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
