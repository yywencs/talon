package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	toolpkg "github.com/wen/opentalon/internal/tool"
	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/config"
	"github.com/wen/opentalon/pkg/prompts"
)

type Agent struct {
	client    LLMClient
	promptReg *prompts.Registry
	promptBld *PromptBuilder
	model     string
	router    *ToolRouter
	tools     []map[string]any // 缓存的工具schema
}

func NewAgent(cfg config.LLMConfig) (*Agent, error) {
	client, err := NewLLMClient(cfg)
	if err != nil {
		return nil, err
	}

	router := NewToolRouter()

	// 缓存所有工具的schema用于function calling
	tools, err := toolpkg.ToolsToOpenAITools(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to build tools schema: %w", err)
	}

	return &Agent{
		client:    client,
		promptReg: prompts.NewRegistry(cfg.PromptsDir),
		promptBld: NewPromptBuilder(),
		model:     cfg.Model,
		router:    router,
		tools:     tools,
	}, nil
}

// Step 执行一次完整的 LLM 循环，并通过回调实时推送事件。
func (a *Agent) Step(ctx context.Context, state *types.SessionState, onEvent func(types.Event)) error {
	systemPrompt := a.promptReg.Get("system_prompt").Base()
	userPrompt := a.promptReg.Get("user_prompt").Base()
	return stepWithLLM(ctx, a.client, a.promptBld, a.model, state, systemPrompt, userPrompt, a.router, a.tools, onEvent)
}

// stepWithLLM 发起一次模型请求并处理返回动作。
func stepWithLLM(ctx context.Context, client LLMClient, promptBld *PromptBuilder, model string, state *types.SessionState, systemPrompt string, userPrompt string, router *ToolRouter, tools []map[string]any, onEvent func(types.Event)) error {
	req := ChatRequest{
		Model:    model,
		Messages: promptBld.BuildMessages(state, systemPrompt, userPrompt),
		Tools:    tools, // 新增：传递工具schema
	}

	resp, err := client.Chat(ctx, req)
	if err != nil {
		return err
	}

	eventCount, err := responseToEvents(ctx, resp, router, onEvent)
	if err != nil {
		return err
	}
	if eventCount == 0 {
		return fmt.Errorf("模型未输出有效动作")
	}
	return nil
}

// responseToEvents 将模型响应转换为事件并通过回调推送。
func responseToEvents(ctx context.Context, resp *ChatResponse, router *ToolRouter, onEvent func(types.Event)) (int, error) {
	if resp == nil {
		return 0, nil
	}
	if onEvent == nil {
		onEvent = func(types.Event) {}
	}
	emitted := 0
	emit := func(event types.Event) {
		if event == nil {
			return
		}
		emitted++
		onEvent(event)
	}

	toolCalls, plainText, err := router.ParseLLMResponse(resp)
	if messageEvent := buildAssistantMessageEvent(resp.Message, plainText); messageEvent != nil {
		emit(messageEvent)
	}

	if err != nil && plainText == "" {
		emit(buildSystemMessageEvent("解析工具调用失败: " + err.Error()))
		return emitted, nil
	}

	if len(toolCalls) == 0 {
		return emitted, nil
	}

	actionEvents, err := router.BuildActionEvents(ctx, toolCalls)
	if err != nil {
		if plainText == "" {
			emit(buildSystemMessageEvent("解析工具调用失败: " + err.Error()))
		}
		return emitted, nil
	}

	emitted += executeActionEvents(ctx, router, actionEvents, onEvent)
	return emitted, nil
}

func buildAssistantMessageEvent(msg types.Message, plainText string) *types.MessageEvent {
	if len(msg.ToolCalls) > 0 {
		msg.ToolCalls = nil
	}
	if plainText != "" {
		msg.Content = []types.Content{
			types.TextContent{Text: plainText},
		}
	}
	if !hasAssistantMessagePayload(msg) {
		return nil
	}
	return &types.MessageEvent{
		BaseEvent: types.BaseEvent{
			ID:        uuid.Must(uuid.NewV7()).String(),
			Timestamp: time.Now(),
			Source:    types.SourceAgent,
		},
		LLMMessage: msg,
	}
}

func buildSystemMessageEvent(text string) *types.MessageEvent {
	return &types.MessageEvent{
		BaseEvent: types.BaseEvent{
			ID:        uuid.Must(uuid.NewV7()).String(),
			Timestamp: time.Now(),
			Source:    types.SourceAgent,
		},
		LLMMessage: types.Message{
			Role: types.RoleAssistant,
			Content: []types.Content{
				types.TextContent{Text: text},
			},
		},
	}
}

func hasAssistantMessagePayload(msg types.Message) bool {
	return len(msg.Content) > 0 ||
		msg.ReasoningContent != "" ||
		len(msg.ThinkingBlocks) > 0 ||
		len(msg.RedactedThinkingBlocks) > 0 ||
		msg.ResponsesReasoningItem != nil
}

// executeActionEvents 并行执行动作，并按输入顺序回调 ObservationEvent。
func executeActionEvents(ctx context.Context, router *ToolRouter, actionEvents []*types.ActionEvent, onEvent func(types.Event)) int {
	if len(actionEvents) == 0 {
		return 0
	}
	if onEvent == nil {
		onEvent = func(types.Event) {}
	}

	maxPar := router.maxPar
	if maxPar <= 0 {
		maxPar = 1
	}

	sem := make(chan struct{}, maxPar)
	var wg sync.WaitGroup
	results := make([]*types.ObservationEvent, len(actionEvents))
	for idx, actionEvent := range actionEvents {
		evt := actionEvent
		resultIdx := idx
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			results[resultIdx] = executeSingleActionEvent(ctx, router, evt)
		}()
	}
	wg.Wait()

	emitted := 0
	for _, observationEvent := range results {
		if observationEvent == nil {
			continue
		}
		emitted++
		onEvent(observationEvent)
	}
	return emitted
}

// executeSingleActionEvent 执行单个动作并转换为 ObservationEvent。
func executeSingleActionEvent(ctx context.Context, router *ToolRouter, event *types.ActionEvent) *types.ObservationEvent {
	if event == nil {
		return nil
	}

	toolCallID := ""
	toolName := ""
	args := []byte("{}")
	if event.ToolCall != nil {
		toolCallID = event.ToolCall.ID
		toolName = event.ToolCall.Name
		args = []byte(event.ToolCall.Arguments)
	}

	observation := executeSingleTool(ctx, router, toolName, args)
	return &types.ObservationEvent{
		BaseEvent: types.BaseEvent{
			ID:        uuid.Must(uuid.NewV7()).String(),
			Timestamp: time.Now(),
			Source:    types.SourceEnvironment,
		},
		ActionID:    event.ActionID,
		ToolName:    toolName,
		Observation: observation,
		ToolCallID:  toolCallID,
	}
}

// executeSingleTool 执行单个工具调用，并统一兜底 unknown/panic 场景。
func executeSingleTool(ctx context.Context, router *ToolRouter, toolName string, arguments []byte) types.Observation {
	toolInstance, err := router.ResolveTool(ctx, toolName)
	if err != nil {
		return &types.BaseObservation{
			ErrorStatus: true,
			Content: []types.Content{
				types.TextContent{Text: fmt.Sprintf("unknown tool: %s", toolName)},
			},
		}
	}

	var observation types.Observation
	func() {
		defer func() {
			if p := recover(); p != nil {
				observation = &types.BaseObservation{
					ErrorStatus: true,
					Content: []types.Content{
						types.TextContent{Text: fmt.Sprintf("panic recovered: %v", p)},
					},
				}
			}
		}()
		observation = toolInstance.Execute(ctx, arguments)
	}()

	return observation
}
