package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/wen/opentalon/internal/tool"
	"github.com/wen/opentalon/internal/types"
)

// ToolRouter 负责将 LLM 返回的工具调用解析并路由到具体工具执行
type ToolRouter struct {
	mu       sync.RWMutex
	registry map[string]tool.ToolFactory // 工具名 → 工厂函数
	maxPar   int                         // 最大并发数
}

// NewToolRouter 创建工具路由器，默认最大并发 10
func NewToolRouter() *ToolRouter {
	return &ToolRouter{
		registry: make(map[string]tool.ToolFactory),
		maxPar:   10,
	}
}

// Register 注册工具工厂函数
func (r *ToolRouter) Register(name string, factory tool.ToolFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registry[name] = factory
}

// WithMaxParallelism 设置最大并发数
func (r *ToolRouter) WithMaxParallelism(n int) *ToolRouter {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.maxPar = n
	return r
}

// ToolCall 表示一个工具调用
type ToolCall struct {
	ID        string          `json:"id"`        // 用于结果对齐
	Name      string          `json:"name"`      // 工具名，如 "bash"
	Arguments json.RawMessage `json:"arguments"` // 原始 JSON 参数
}

// ParsedTools 表示解析后的工具调用结果
type ParsedTools struct {
	Tools        []ToolCall `json:"tools"`
	PlainMessage string     `json:"plain_message,omitempty"` // 无工具时的纯文本
}

// ExecutionResult 表示工具执行结果
type ExecutionResult struct {
	Results []ExecutionItem  `json:"results"`
	Errors  []ExecutionError `json:"errors,omitempty"`
}

// ExecutionItem 表示单个工具执行结果
type ExecutionItem struct {
	ToolName string `json:"tool_name"`
	Success  bool   `json:"success"`
	Output   string `json:"output,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
}

// ExecutionError 表示执行错误
type ExecutionError struct {
	ToolName string `json:"tool_name"`
	Error    string `json:"error"`
}

// ParseLLMResponse 解析 LLM 返回内容，支持两种格式：
// 1. 原生 Tool Calls（推荐）
// 2. 旧版 XML 标签格式（兼容）
func (r *ToolRouter) ParseLLMResponse(resp *ChatResponse) (*ParsedTools, error) {
	if resp == nil {
		return nil, fmt.Errorf("nil LLM response")
	}

	var result ParsedTools

	// 回退到 XML 标签解析（兼容旧格式）
	content := strings.TrimSpace(resp.Content)
	if content == "" {
		return nil, fmt.Errorf("empty LLM response")
	}

	// 提取工具调用
	toolPattern := regexp.MustCompile(`(?i)<execute_(\w+)>(?s)(.+?)</execute_\w+>`)
	matches := toolPattern.FindAllStringSubmatch(content, -1)

	for i, match := range matches {
		if len(match) >= 3 {
			toolName := strings.ToLower(match[1])
			toolArgs := strings.TrimSpace(match[2])

			// 根据工具名创建对应的 Action 结构
			actionData, err := r.createActionData(toolName, toolArgs)
			if err != nil {
				continue // 跳过无效的工具调用
			}

			result.Tools = append(result.Tools, ToolCall{
				ID:        fmt.Sprintf("xml_%d", i), // 为 XML 格式生成 ID
				Name:      toolName,
				Arguments: actionData,
			})
		}
	}

	// 提取纯文本消息（去掉所有工具调用标签后的内容）
	plainText := toolPattern.ReplaceAllString(content, "")
	plainText = strings.TrimSpace(plainText)
	if plainText != "" {
		result.PlainMessage = plainText
	}

	return &result, nil
}

// createActionData 根据工具名创建对应的 Action JSON 数据
func (r *ToolRouter) createActionData(toolName, args string) (json.RawMessage, error) {
	switch toolName {
	case "bash":
		action := tool.BashAction{
			ActionMetadata: tool.ActionMetadata{
				Summary:      "Execute bash command",
				SecurityRisk: tool.SecurityRisk_HIGH,
			},
			Command: args,
		}
		return json.Marshal(action)
	default:
		// 对于未知工具，返回通用格式
		return json.Marshal(map[string]interface{}{
			"command": args,
		})
	}
}

// ExecuteTools 并发执行工具调用
// 注意：单个工具失败不会中断整个批次，所有结果都会被收集
func (r *ToolRouter) ExecuteTools(ctx context.Context, calls []ToolCall) *ExecutionResult {
	if len(calls) == 0 {
		return &ExecutionResult{}
	}

	result := &ExecutionResult{
		Results: make([]ExecutionItem, 0, len(calls)),
		Errors:  make([]ExecutionError, 0),
	}

	// 使用 WaitGroup 和 channel 实现并发控制
	var wg sync.WaitGroup
	resultCh := make(chan ExecutionItem, len(calls))
	errorCh := make(chan ExecutionError, len(calls))

	// 信号量控制并发数
	sem := make(chan struct{}, r.maxPar)

	for _, call := range calls {
		wg.Add(1)
		go func(tc ToolCall) {
			defer wg.Done()

			// 获取信号量
			sem <- struct{}{}
			defer func() { <-sem }()

			// 检查 context 是否已取消
			select {
			case <-ctx.Done():
				errorCh <- ExecutionError{
					ToolName: tc.Name,
					Error:    "context cancelled",
				}
				return
			default:
			}

			// 执行工具
			item, execErr := r.executeSingleTool(ctx, tc)

			if execErr != nil {
				errorCh <- ExecutionError{
					ToolName: tc.Name,
					Error:    execErr.Error(),
				}
			} else {
				resultCh <- item
			}
		}(call)
	}

	// 等待所有 goroutine 完成
	go func() {
		wg.Wait()
		close(resultCh)
		close(errorCh)
	}()

	// 收集结果
	for item := range resultCh {
		result.Results = append(result.Results, item)
	}
	for err := range errorCh {
		result.Errors = append(result.Errors, err)
	}

	return result
}

// executeSingleTool 执行单个工具调用
func (r *ToolRouter) executeSingleTool(ctx context.Context, call ToolCall) (ExecutionItem, error) {
	// 获取工具工厂
	r.mu.RLock()
	factory, exists := r.registry[call.Name]
	r.mu.RUnlock()

	if !exists {
		return ExecutionItem{}, fmt.Errorf("unknown tool: %s", call.Name)
	}

	// 创建工具实例
	toolInstance := factory(ctx)

	// 执行工具（带 panic 保护）
	var obs types.Observation
	func() {
		defer func() {
			if r := recover(); r != nil {
				// panic 时返回错误 Observation
				obs = tool.NewTerminalObservation("", "", nil, false, -1, fmt.Sprintf("panic recovered: %v", r))
			}
		}()
		obs = toolInstance.Execute(ctx, call.Arguments)
	}()

	output := types.FlattenTextContent(obs.GetContent())
	exitCode := 0
	if cmdObs, ok := obs.(*tool.TerminalObservation); ok {
		exitCode = cmdObs.ExitCodeValue()
		if output == "" {
			output = cmdObs.OutputText()
		}
	}

	return ExecutionItem{
		ToolName: call.Name,
		Success:  !obs.IsError(),
		Output:   output,
		ExitCode: exitCode,
	}, nil
}

// ConvertToObservations 将 ExecutionResult 转换为 Observation 列表供 Agent 使用
func (r *ToolRouter) ConvertToObservations(result *ExecutionResult) []types.Observation {
	if result == nil {
		return nil
	}

	var observations []types.Observation

	// 添加成功结果
	for _, item := range result.Results {
		if item.Success {
			observations = append(observations, tool.NewTerminalObservation("", "", nil, false, item.ExitCode, fmt.Sprintf("[%s] 执行成功:\n%s", item.ToolName, item.Output)))
		} else {
			observations = append(observations, tool.NewTerminalObservation("", "", nil, false, item.ExitCode, fmt.Sprintf("[%s] 执行失败 (exit %d):\n%s", item.ToolName, item.ExitCode, item.Output)))
		}
	}

	// 添加错误信息
	for _, err := range result.Errors {
		observations = append(observations, tool.NewTerminalObservation("", "", nil, false, -1, fmt.Sprintf("[%s] 错误: %s", err.ToolName, err.Error)))
	}

	return observations
}
