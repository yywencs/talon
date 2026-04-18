package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/invopop/jsonschema"
	"github.com/wen/opentalon/internal/types"
)

type Observation = types.Observation

type Tool interface {
	Name() string
	Description() string
	GetActionDef() any
	Execute(ctx context.Context, rawArgs []byte) Observation
}

type BaseTool[A types.Action, O types.Observation] struct {
	ToolName string
	ToolDesc string
	Executor func(ctx context.Context, action A) O
}

func (t *BaseTool[A, O]) Name() string {
	return t.ToolName
}

func (t *BaseTool[A, O]) Description() string {
	return t.ToolDesc
}

func (b *BaseTool[A, O]) GetActionDef() any {
	var a A
	return a
}

func (t *BaseTool[A, O]) Execute(ctx context.Context, rawArgs []byte) Observation {
	var action A
	if err := json.Unmarshal(rawArgs, &action); err != nil {
		return &types.BaseObservation{
			Content: []types.Content{
				types.TextContent{
					Text: "invalid JSON arguments: " + err.Error(),
				},
			},
			ErrorStatus: true,
		}
	}

	result := t.Executor(ctx, action)
	return result
}

func ToOpenAITool(t Tool) (map[string]any, error) {
	actionDef := t.GetActionDef()
	if actionDef == nil {
		return nil, fmt.Errorf("tool %s GetActionDef returned nil", t.Name())
	}

	r := new(jsonschema.Reflector)
	r.DoNotReference = true
	schema := r.Reflect(actionDef)

	var schemaMap map[string]any
	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}
	if err := json.Unmarshal(schemaBytes, &schemaMap); err != nil {
		return nil, fmt.Errorf("unmarshal schema: %w", err)
	}

	if schemaMap["type"] == nil {
		schemaMap["type"] = "object"
	}
	if schemaMap["properties"] == nil {
		schemaMap["properties"] = map[string]any{}
	}

	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        t.Name(),
			"description": t.Description(),
			"parameters":  schemaMap,
		},
	}, nil
}

func ToolsToOpenAITools(ctx context.Context) ([]map[string]any, error) {
	names := List()
	tools := make([]map[string]any, 0, len(names))
	for _, name := range names {
		factory, ok := Get(name)
		if !ok {
			continue
		}
		t := factory(ctx)
		converted, err := ToOpenAITool(t)
		if err != nil {
			return nil, fmt.Errorf("convert tool %s: %w", name, err)
		}
		tools = append(tools, converted)
	}
	return tools, nil
}
