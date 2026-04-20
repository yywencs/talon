package serializer

import (
	"testing"

	"github.com/wen/opentalon/internal/types"
)

func TestSerializeMessagesWithTextCacheControl(t *testing.T) {
	messageSerializer := &OpenAIChatSerializer{
		CacheEnabled: true,
	}

	result, err := SerializeMessages(messageSerializer, []types.Message{
		{
			Role: types.RoleSystem,
			Content: []types.Content{
				types.TextContent{
					BaseContent: types.BaseContent{CachePrompt: true},
					Text:        "system prompt",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("serialize messages failed: %v", err)
	}

	payload, ok := result[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected payload: %#v", result[0])
	}
	if _, exists := payload["cache_control"]; exists {
		t.Fatalf("cache_control should not appear on message top level: %#v", payload)
	}

	content, ok := payload["content"].([]map[string]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content should contain a single block: %#v", payload["content"])
	}
	if content[0]["type"] != "text" || content[0]["text"] != "system prompt" {
		t.Fatalf("unexpected text block: %#v", content[0])
	}

	cacheControl, ok := content[0]["cache_control"].(map[string]string)
	if !ok || cacheControl["type"] != "ephemeral" {
		t.Fatalf("cache_control should appear inside content block: %#v", content[0])
	}
}

func TestSerializeResponsesToolCall(t *testing.T) {
	messageSerializer := &OpenAIChatSerializer{
		FunctionCallingEnabled: true,
	}

	item, err := messageSerializer.Serialize(types.Message{
		Role: types.RoleAssistant,
		ToolCalls: []types.MessageToolCall{
			{
				ID:        "123",
				Name:      "bash",
				Arguments: `{"command":"pwd"}`,
				Origin:    types.OriginResponses,
			},
		},
	})
	if err != nil {
		t.Fatalf("serialize message failed: %v", err)
	}

	payload, ok := item.(map[string]any)
	if !ok {
		t.Fatalf("unexpected payload: %#v", item)
	}
	toolCalls, ok := payload["tool_calls"].([]map[string]any)
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("unexpected tool calls: %#v", payload["tool_calls"])
	}
	if toolCalls[0]["id"] != "fc_123" || toolCalls[0]["call_id"] != "fc_123" {
		t.Fatalf("unexpected responses id fields: %#v", toolCalls[0])
	}
	if toolCalls[0]["arguments"] != `{"command":"pwd"}` {
		t.Fatalf("unexpected arguments: %#v", toolCalls[0])
	}
}

func TestSerializeListWithReasoningContent(t *testing.T) {
	messageSerializer := &OpenAIChatSerializer{
		FunctionCallingEnabled: true,
		SendReasoningContent:   true,
	}

	item, err := messageSerializer.Serialize(types.Message{
		Role:             types.RoleAssistant,
		ReasoningContent: "先分析目录，再决定是否调用工具。",
	})
	if err != nil {
		t.Fatalf("serialize message failed: %v", err)
	}

	payload, ok := item.(map[string]any)
	if !ok {
		t.Fatalf("unexpected payload: %#v", item)
	}
	if payload["role"] != types.RoleAssistant {
		t.Fatalf("unexpected role: %#v", payload["role"])
	}
	if payload["reasoning_content"] != "先分析目录，再决定是否调用工具。" {
		t.Fatalf("unexpected reasoning_content: %#v", payload["reasoning_content"])
	}
	if _, exists := payload["content"]; exists {
		t.Fatalf("content should be omitted for reasoning-only assistant message: %#v", payload)
	}
}

func TestSerializeImageContentCacheControl(t *testing.T) {
	messageSerializer := &OpenAIChatSerializer{
		VisionEnabled: true,
	}

	item, err := messageSerializer.Serialize(types.Message{
		Role: types.RoleUser,
		Content: []types.Content{
			types.ImageContent{
				BaseContent: types.BaseContent{CachePrompt: true},
				ImageURLs:   []string{"https://example.com/a.png", "https://example.com/b.png"},
			},
		},
	})
	if err != nil {
		t.Fatalf("serialize message failed: %v", err)
	}

	payload, ok := item.(map[string]any)
	if !ok {
		t.Fatalf("unexpected payload: %#v", item)
	}
	content, ok := payload["content"].([]map[string]any)
	if !ok || len(content) != 2 {
		t.Fatalf("unexpected content blocks: %#v", payload["content"])
	}
	last := content[1]
	if last["type"] != "image_url" {
		t.Fatalf("unexpected block type: %#v", last)
	}
	if _, ok := last["cache_control"]; !ok {
		t.Fatalf("expected cache control on last image block: %#v", last)
	}
}
