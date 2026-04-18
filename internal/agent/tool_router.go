package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wen/opentalon/internal/tool"
	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/utils"
)

type ToolRouter struct {
	mu       sync.RWMutex
	registry map[string]tool.ToolFactory
	maxPar   int
}

func NewToolRouter() *ToolRouter {
	return &ToolRouter{
		registry: make(map[string]tool.ToolFactory),
		maxPar:   10,
	}
}

func (r *ToolRouter) Register(name string, factory tool.ToolFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registry[name] = factory
}

func (r *ToolRouter) WithMaxParallelism(n int) *ToolRouter {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.maxPar = n
	return r
}

func (r *ToolRouter) ResolveAllTools(ctx context.Context) map[string]tool.Tool {
	return tool.ResolveAll(ctx)
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolCallAction struct {
	ToolCalls []ToolCall
	PlainText string
	Router    *ToolRouter
}

func (a *ToolCallAction) ActionType() types.ActionType {
	return types.ActionRun
}

func (r *ToolRouter) ParseLLMResponse(resp *ChatResponse) ([]ToolCall, string, error) {
	if resp == nil {
		return nil, "", fmt.Errorf("nil LLM response")
	}

	plainText := utils.FlattenTextContent(resp.Message.Content)
	if len(resp.Message.ToolCalls) == 0 {
		return nil, plainText, nil
	}

	calls := make([]ToolCall, 0, len(resp.Message.ToolCalls))
	for _, tc := range resp.Message.ToolCalls {
		calls = append(calls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Name,
			Arguments: json.RawMessage(tc.Arguments),
		})
	}
	return calls, plainText, nil
}

func (r *ToolRouter) BuildActionEvents(ctx context.Context, calls []ToolCall) ([]*types.ActionEvent, error) {
	if len(calls) == 0 {
		return nil, nil
	}

	events := make([]*types.ActionEvent, 0, len(calls))
	for _, call := range calls {
		action, err := r.decodeToolAction(ctx, call)
		if err != nil {
			return nil, err
		}

		actionID := uuid.Must(uuid.NewV7()).String()
		toolCall := types.MessageToolCall{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: string(call.Arguments),
		}
		events = append(events, &types.ActionEvent{
			BaseEvent: types.BaseEvent{
				ID:        actionID,
				Timestamp: time.Now(),
				Source:    types.SourceAgent,
			},
			ActionID:   actionID,
			ActionType: action.ActionType(),
			Action:     action,
			ToolCall:   &toolCall,
		})
	}
	return events, nil
}

// ResolveTool 根据工具名解析出可执行实例，执行由 Agent 负责。
func (r *ToolRouter) ResolveTool(ctx context.Context, name string) (tool.Tool, error) {
	r.mu.RLock()
	factory, exists := r.registry[name]
	r.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
	return factory(ctx), nil
}

func (r *ToolRouter) decodeToolAction(ctx context.Context, call ToolCall) (types.Action, error) {
	r.mu.RLock()
	factory, exists := r.registry[call.Name]
	r.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("unknown tool: %s", call.Name)
	}

	toolInstance := factory(ctx)
	actionDef := toolInstance.GetActionDef()
	if actionDef == nil {
		return nil, fmt.Errorf("tool %s action definition is nil", call.Name)
	}

	target, err := newActionTarget(actionDef)
	if err != nil {
		return nil, fmt.Errorf("prepare tool %s action target: %w", call.Name, err)
	}
	if err := json.Unmarshal(call.Arguments, target); err != nil {
		return nil, fmt.Errorf("decode tool %s arguments: %w", call.Name, err)
	}

	action, ok := extractAction(target)
	if !ok {
		return nil, fmt.Errorf("tool %s action does not implement types.Action", call.Name)
	}
	return action, nil
}

func newActionTarget(actionDef any) (any, error) {
	defType := reflect.TypeOf(actionDef)
	if defType == nil {
		return nil, fmt.Errorf("nil action type")
	}
	if defType.Kind() == reflect.Ptr {
		return reflect.New(defType.Elem()).Interface(), nil
	}
	return reflect.New(defType).Interface(), nil
}

func extractAction(target any) (types.Action, bool) {
	if action, ok := target.(types.Action); ok {
		return action, true
	}

	value := reflect.ValueOf(target)
	if !value.IsValid() || value.Kind() != reflect.Ptr || value.IsNil() {
		return nil, false
	}

	action, ok := value.Elem().Interface().(types.Action)
	return action, ok
}
