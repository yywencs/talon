package tool

import (
	"context"
	"encoding/json"
	"testing"
)

func TestToOpenAITool(t *testing.T) {
	factory, ok := Get("bash")
	if !ok {
		t.Fatal("bash tool not registered")
	}
	toolInstance := factory(context.Background())

	result, err := ToOpenAITool(toolInstance)
	if err != nil {
		t.Fatalf("ToOpenAITool failed: %v", err)
	}

	if result["type"] != "function" {
		t.Fatalf("expected type=function, got %v", result["type"])
	}

	fn, ok := result["function"].(map[string]any)
	if !ok {
		t.Fatalf("function field should be map, got %T", result["function"])
	}

	if fn["name"] != "bash" {
		t.Fatalf("expected name=bash, got %v", fn["name"])
	}

	if fn["description"] == nil || fn["description"] == "" {
		t.Fatal("description should not be empty")
	}

	params, ok := fn["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters should be map, got %T", fn["parameters"])
	}

	if params["$defs"] != nil {
		defs, ok := params["$defs"].(map[string]any)
		if !ok {
			t.Fatalf("$defs should be map, got %T", params["$defs"])
		}
		if _, ok := defs["BashAction"]; !ok {
			t.Fatal("$defs should contain BashAction")
		}
	}
}

func TestToOpenAITool_NilActionDef(t *testing.T) {
	bad := &badTool{}
	_, err := ToOpenAITool(bad)
	if err == nil {
		t.Fatal("expected error for nil action def")
	}
}

func TestToolsToOpenAITools(t *testing.T) {
	tools, err := ToolsToOpenAITools(context.Background())
	if err != nil {
		t.Fatalf("ToolsToOpenAITools failed: %v", err)
	}

	if len(tools) == 0 {
		t.Fatal("expected at least one tool")
	}

	for _, tool := range tools {
		if tool["type"] != "function" {
			t.Fatalf("expected type=function, got %v", tool["type"])
		}
		fn := tool["function"].(map[string]any)
		if fn["name"] == nil || fn["name"] == "" {
			t.Fatal("tool name should not be empty")
		}
	}
}

func TestToOpenAITool_JSON(t *testing.T) {
	factory, _ := Get("bash")
	toolInstance := factory(context.Background())

	result, err := ToOpenAITool(toolInstance)
	if err != nil {
		t.Fatalf("ToOpenAITool failed: %v", err)
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	fn := decoded["function"].(map[string]any)
	params := fn["parameters"].(map[string]any)
	if params == nil {
		t.Fatal("parameters should not be nil")
	}
}

type badTool struct{}

func (b *badTool) Name() string        { return "bad" }
func (b *badTool) Description() string { return "bad tool" }
func (b *badTool) GetActionDef() any   { return nil }
func (b *badTool) Execute(ctx context.Context, rawArgs []byte) Observation {
	return nil
}
