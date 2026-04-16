package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wen/opentalon/internal/tool"
	"github.com/wen/opentalon/internal/types"
)

func TestToolRouter_SingleBashTool(t *testing.T) {
	router := NewToolRouter()
	// 注册 bash 工具
	factory, ok := tool.Get("bash")
	if !ok {
		t.Fatal("bash tool not registered")
	}
	router.Register("bash", factory)

	// 模拟 LLM 响应：执行 bash 命令（XML 格式）
	resp := &ChatResponse{
		Content: "<execute_bash>echo hello</execute_bash>",
	}

	parsed, err := router.ParseLLMResponse(resp)
	if err != nil {
		t.Fatalf("ParseLLMResponse failed: %v", err)
	}

	if len(parsed.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(parsed.Tools))
	}

	if parsed.Tools[0].Name != "bash" {
		t.Fatalf("expected bash tool, got %s", parsed.Tools[0].Name)
	}

	// 执行工具
	result := router.ExecuteTools(context.Background(), parsed.Tools)

	if len(result.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.Results))
	}

	if !result.Results[0].Success {
		t.Fatalf("expected success, got failure: %s", result.Results[0].Output)
	}

	if !strings.Contains(result.Results[0].Output, "hello") {
		t.Fatalf("expected output to contain 'hello', got: %s", result.Results[0].Output)
	}
}

func TestToolRouter_MultipleTools(t *testing.T) {
	router := NewToolRouter()
	// 注册 bash 工具
	factory, ok := tool.Get("bash")
	if !ok {
		t.Fatal("bash tool not registered")
	}
	router.Register("bash", factory)

	// 模拟多个工具调用（XML 格式）
	resp := &ChatResponse{
		Content: "<execute_bash>echo 1</execute_bash>\n<execute_bash>echo 2</execute_bash>",
	}

	parsed, err := router.ParseLLMResponse(resp)
	if err != nil {
		t.Fatalf("ParseLLMResponse failed: %v", err)
	}

	if len(parsed.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(parsed.Tools))
	}

	// 并发执行
	result := router.ExecuteTools(context.Background(), parsed.Tools)

	if len(result.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.Results))
	}

	// 验证两个结果
	outputs := make(map[string]bool)
	for _, item := range result.Results {
		if !item.Success {
			t.Errorf("expected success for %s, got failure: %s", item.ToolName, item.Output)
		}
		outputs[item.Output] = true
	}

	if !outputs["1\n"] || !outputs["2\n"] {
		t.Fatalf("expected outputs to contain '1\\n' and '2\\n', got: %v", outputs)
	}
}

func TestToolRouter_PlainMessage(t *testing.T) {
	router := NewToolRouter()

	// 纯文本消息
	resp := &ChatResponse{
		Content: "这是一条普通消息",
	}

	parsed, err := router.ParseLLMResponse(resp)
	if err != nil {
		t.Fatalf("ParseLLMResponse failed: %v", err)
	}

	if len(parsed.Tools) != 0 {
		t.Fatalf("expected 0 tools, got %d", len(parsed.Tools))
	}

	if parsed.PlainMessage != "这是一条普通消息" {
		t.Fatalf("expected plain message, got: %s", parsed.PlainMessage)
	}
}

func TestToolRouter_MixedContent(t *testing.T) {
	router := NewToolRouter()

	// 混合内容：文本 + 工具
	resp := &ChatResponse{
		Content: "先执行这个命令\n<execute_bash>echo mixed</execute_bash>",
	}

	parsed, err := router.ParseLLMResponse(resp)
	if err != nil {
		t.Fatalf("ParseLLMResponse failed: %v", err)
	}

	if len(parsed.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(parsed.Tools))
	}

	if parsed.PlainMessage != "先执行这个命令" {
		t.Fatalf("expected plain message, got: %s", parsed.PlainMessage)
	}
}

func TestToolRouter_XMLFormat(t *testing.T) {
	router := NewToolRouter()

	// XML 格式
	resp := &ChatResponse{
		Content: "<execute_bash>echo xml format</execute_bash>",
	}

	parsed, err := router.ParseLLMResponse(resp)
	if err != nil {
		t.Fatalf("ParseLLMResponse failed: %v", err)
	}

	if len(parsed.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(parsed.Tools))
	}

	if parsed.Tools[0].Name != "bash" {
		t.Fatalf("expected bash tool, got %s", parsed.Tools[0].Name)
	}
}

func TestToolRouter_UnknownTool(t *testing.T) {
	router := NewToolRouter()

	// 未知工具
	resp := &ChatResponse{
		Content: "<execute_unknown>test</execute_unknown>",
	}

	parsed, err := router.ParseLLMResponse(resp)
	if err != nil {
		t.Fatalf("ParseLLMResponse failed: %v", err)
	}

	// 执行未知工具
	result := router.ExecuteTools(context.Background(), parsed.Tools)

	if len(result.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(result.Errors))
	}

	if !strings.Contains(result.Errors[0].Error, "unknown tool") {
		t.Fatalf("expected 'unknown tool' error, got: %s", result.Errors[0].Error)
	}
}

func TestToolRouter_ConcurrentLimit(t *testing.T) {
	router := NewToolRouter().WithMaxParallelism(2) // 限制并发数为 2
	// 注册 bash 工具
	factory, ok := tool.Get("bash")
	if !ok {
		t.Fatal("bash tool not registered")
	}
	router.Register("bash", factory)

	// 创建 5 个工具调用
	var calls []ToolCall
	for i := 1; i <= 5; i++ {
		cmd := fmt.Sprintf("echo %d", i)
		args, _ := json.Marshal(map[string]interface{}{
			"command": cmd,
			"ActionMetadata": map[string]string{
				"Summary":      "test",
				"SecurityRisk": "HIGH",
			},
		})
		calls = append(calls, ToolCall{
			ID:        fmt.Sprintf("call_%d", i),
			Name:      "bash",
			Arguments: args,
		})
	}

	start := time.Now()
	result := router.ExecuteTools(context.Background(), calls)
	elapsed := time.Since(start)

	if len(result.Results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(result.Results))
	}

	// 由于并发限制，执行时间应该比串行快但比完全并发慢
	// 这里主要验证没有死锁和数据竞争
	if elapsed < 100*time.Millisecond {
		t.Logf("Warning: execution too fast (%v), may indicate concurrency issue", elapsed)
	}
}

func TestToolRouter_ContextCancellation(t *testing.T) {
	router := NewToolRouter()
	// 注册 bash 工具
	factory, ok := tool.Get("bash")
	if !ok {
		t.Fatal("bash tool not registered")
	}
	router.Register("bash", factory)

	// 创建长时间运行的命令
	calls := []ToolCall{
		{
			ID:        "call_1",
			Name:      "bash",
			Arguments: json.RawMessage(`{"command":"sleep 10","ActionMetadata":{"Summary":"long test","SecurityRisk":"HIGH"}}`),
		},
	}

	ctx, cancel := context.WithCancel(context.Background())

	// 100ms 后取消 context
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	result := router.ExecuteTools(ctx, calls)

	// 检查是否被取消 - 可能有错误或者结果被标记为失败
	hasError := len(result.Errors) > 0
	hasFailedResult := false
	for _, item := range result.Results {
		if !item.Success {
			hasFailedResult = true
			break
		}
	}

	if !hasError && !hasFailedResult {
		t.Fatalf("expected cancellation to cause error or failed result, got %d errors and %d results (success: %v)",
			len(result.Errors), len(result.Results),
			len(result.Results) > 0 && result.Results[0].Success)
	}

	// 验证错误信息包含取消相关关键词
	if hasError && !strings.Contains(result.Errors[0].Error, "cancelled") && !strings.Contains(result.Errors[0].Error, "killed") {
		t.Logf("cancellation error: %s", result.Errors[0].Error)
	}
	if hasFailedResult && len(result.Results) > 0 {
		resultItem := result.Results[0]
		if !strings.Contains(resultItem.Output, "cancelled") && !strings.Contains(resultItem.Output, "killed") && !strings.Contains(resultItem.Output, "timed out") {
			t.Logf("cancellation result output: %s", resultItem.Output)
		}
	}
}

func TestToolRouter_ConvertToObservations(t *testing.T) {
	router := NewToolRouter()

	result := &ExecutionResult{
		Results: []ExecutionItem{
			{
				ToolName: "bash",
				Success:  true,
				Output:   "hello world",
				ExitCode: 0,
			},
			{
				ToolName: "bash",
				Success:  false,
				Output:   "command not found",
				ExitCode: 127,
			},
		},
		Errors: []ExecutionError{
			{
				ToolName: "unknown",
				Error:    "unknown tool: unknown",
			},
		},
	}

	observations := router.ConvertToObservations(result)

	if len(observations) != 3 {
		t.Fatalf("expected 3 observations, got %d", len(observations))
	}

	// 验证成功消息
	if !strings.Contains(observations[0].(*tool.TerminalObservation).OutputText(), "执行成功") {
		t.Errorf("expected success message, got: %s", observations[0].(*tool.TerminalObservation).OutputText())
	}

	// 验证失败消息
	if !strings.Contains(observations[1].(*tool.TerminalObservation).OutputText(), "执行失败") {
		t.Errorf("expected failure message, got: %s", observations[1].(*tool.TerminalObservation).OutputText())
	}

	// 验证错误消息
	if !strings.Contains(observations[2].(*tool.TerminalObservation).OutputText(), "错误") {
		t.Errorf("expected error message, got: %s", observations[2].(*tool.TerminalObservation).OutputText())
	}
}

func TestToolRouter_ConcurrentSafety(t *testing.T) {
	router := NewToolRouter()

	// 并发注册工具
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			router.Register(fmt.Sprintf("tool_%d", idx), func(ctx context.Context) tool.Tool {
				return &mockTool{name: fmt.Sprintf("tool_%d", idx)}
			})
		}(i)
	}

	// 并发解析和执行
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			calls := []ToolCall{
				{
					ID:        fmt.Sprintf("call_%d", idx),
					Name:      "bash",
					Arguments: json.RawMessage(`{"command":"echo test"}`),
				},
			}
			router.ExecuteTools(context.Background(), calls)
		}(i)
	}

	wg.Wait()
	// 如果没有 panic，说明并发安全
}

// mockTool 用于测试的模拟工具
type mockTool struct {
	name string
}

func (m *mockTool) Name() string {
	return m.name
}

func (m *mockTool) Description() string {
	return "mock tool"
}

func (m *mockTool) Execute(ctx context.Context, rawArgs []byte) types.Observation {
	return tool.NewTerminalObservation("", "", nil, false, 0, "mock result")
}
