package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/wen/opentalon/internal/tool"
	"github.com/wen/opentalon/internal/types"
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

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (r *ToolRouter) ParseLLMResponse(resp *ChatResponse) ([]ToolCall, string, error) {
	if resp == nil {
		return nil, "", fmt.Errorf("nil LLM response")
	}

	plainText := resp.Content
	if len(resp.ToolCalls) == 0 {
		return nil, plainText, nil
	}

	calls := make([]ToolCall, 0, len(resp.ToolCalls))
	for _, tc := range resp.ToolCalls {
		calls = append(calls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Name,
			Arguments: json.RawMessage(tc.Arguments),
		})
	}
	return calls, plainText, nil
}

func (r *ToolRouter) ExecuteTools(ctx context.Context, calls []ToolCall) []types.Observation {
	if len(calls) == 0 {
		return nil
	}

	results := make([]types.Observation, len(calls))
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, r.maxPar)

	for i, call := range calls {
		wg.Add(1)
		go func(idx int, tc ToolCall) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			obs := r.executeSingleTool(ctx, tc)
			mu.Lock()
			results[idx] = obs
			mu.Unlock()
		}(i, call)
	}

	wg.Wait()
	return results
}

func (r *ToolRouter) executeSingleTool(ctx context.Context, call ToolCall) types.Observation {
	r.mu.RLock()
	factory, exists := r.registry[call.Name]
	r.mu.RUnlock()

	if !exists {
		return &types.BaseObservation{
			ErrorStatus: true,
			Content: []types.Content{
				types.TextContent{Text: fmt.Sprintf("unknown tool: %s", call.Name)},
			},
		}
	}

	toolInstance := factory(ctx)

	var obs types.Observation
	func() {
		defer func() {
			if p := recover(); p != nil {
				obs = &types.BaseObservation{
					ErrorStatus: true,
					Content: []types.Content{
						types.TextContent{Text: fmt.Sprintf("panic recovered: %v", p)},
					},
				}
			}
		}()
		obs = toolInstance.Execute(ctx, call.Arguments)
	}()

	return obs
}
