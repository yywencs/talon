package core

import (
	"context"
	"fmt"
	"sync"

	"github.com/wen/opentalon/internal/tool"
	"github.com/wen/opentalon/internal/types"
)

type ToolRouter struct {
	mu           sync.RWMutex
	registry     map[string]tool.ToolFactory
	eventFactory *EventFactory
}

func NewToolRouter() *ToolRouter {
	return &ToolRouter{
		registry:     make(map[string]tool.ToolFactory),
		eventFactory: NewEventFactory(),
	}
}

func (r *ToolRouter) Register(name string, factory tool.ToolFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registry[name] = factory
}

func (r *ToolRouter) ResolveAllTools(ctx context.Context) map[string]tool.Tool {
	return tool.ResolveAll(ctx)
}

type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolCallAction struct {
	ToolCalls []ToolCall
	PlainText string
	Router    *ToolRouter
}

func (a *ToolCallAction) ActionType() types.ActionType {
	return types.ActionRun
}

func (r *ToolRouter) BuildActionEvents(ctx context.Context, calls []ToolCall) ([]*types.ActionEvent, error) {
	if len(calls) == 0 {
		return nil, nil
	}

	messageToolCalls := make([]types.MessageToolCall, 0, len(calls))
	for _, call := range calls {
		messageToolCalls = append(messageToolCalls, types.MessageToolCall{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: call.Arguments,
		})
	}
	return r.eventFactory.BuildActionEvents(messageToolCalls, types.SourceAgent, "")
}

// ResolveTool 根据工具名解析出可执行实例，执行由 Session 负责。
func (r *ToolRouter) ResolveTool(ctx context.Context, name string) (tool.Tool, error) {
	r.mu.RLock()
	factory, exists := r.registry[name]
	r.mu.RUnlock()

	if exists {
		return factory(ctx), nil
	}
	return tool.Resolve(name, ctx)
}

// ExecuteActionEvent 执行单个动作并转换为 ObservationEvent。
func (r *ToolRouter) ExecuteActionEvent(ctx context.Context, event *types.ActionEvent) *types.ObservationEvent {
	if event == nil {
		return nil
	}

	toolCallID := event.ToolCallID
	toolName := event.ToolName
	args := []byte("{}")
	if event.ToolCall != nil {
		if toolCallID == "" {
			toolCallID = event.ToolCall.ID
		}
		if toolName == "" {
			toolName = event.ToolCall.Name
		}
		args = []byte(event.ToolCall.Arguments)
	}

	observation := r.executeSingleTool(ctx, toolName, args)
	return r.eventFactory.NewObservationEvent(event, observation, toolName, toolCallID)
}

// ExecuteBatch 并发执行动作，并按输入顺序返回 ObservationEvent。
func (r *ToolRouter) ExecuteBatch(ctx context.Context, actionEvents []*types.ActionEvent, parallelism int) []*types.ObservationEvent {
	if len(actionEvents) == 0 {
		return nil
	}
	if parallelism <= 0 {
		parallelism = 1
	}

	sem := make(chan struct{}, parallelism)
	results := make([]*types.ObservationEvent, len(actionEvents))
	var wg sync.WaitGroup

	for idx, actionEvent := range actionEvents {
		event := actionEvent
		resultIdx := idx
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			results[resultIdx] = r.ExecuteActionEvent(ctx, event)
		}()
	}
	wg.Wait()
	return results
}

// executeSingleTool 执行单个工具调用，并统一兜底 unknown/panic 场景。
func (r *ToolRouter) executeSingleTool(ctx context.Context, toolName string, arguments []byte) types.Observation {
	toolInstance, err := r.ResolveTool(ctx, toolName)
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
