package types

import "testing"

func TestMessageToolCallFromChatToolCall(t *testing.T) {
	call, err := MessageToolCallFromChatToolCall(ChatToolCallInput{
		ID:   "call_1",
		Type: "function",
		Function: &ChatToolCallFunction{
			Name:      "bash",
			Arguments: `{"command":"ls"}`,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if call.ID != "call_1" || call.Name != "bash" || call.Arguments != `{"command":"ls"}` || call.Origin != OriginCompletion {
		t.Fatalf("unexpected tool call: %+v", call)
	}
}

func TestMessageToolCallFromResponsesFunctionCall(t *testing.T) {
	call, err := MessageToolCallFromResponsesFunctionCall(ResponsesFunctionCallInput{
		CallID:    "fc_123",
		Name:      "bash",
		Arguments: `{"command":"pwd"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if call.ID != "fc_123" || call.Name != "bash" || call.Origin != OriginResponses {
		t.Fatalf("unexpected tool call: %+v", call)
	}
}

func TestMessageToolCallToResponsesDict(t *testing.T) {
	call := MessageToolCall{
		ID:        "123",
		Name:      "bash",
		Arguments: `{"command":"pwd"}`,
	}

	result := call.ToResponsesDict()
	if result["id"] != "fc_123" || result["call_id"] != "fc_123" {
		t.Fatalf("unexpected responses id fields: %+v", result)
	}
	if result["arguments"] != `{"command":"pwd"}` {
		t.Fatalf("unexpected arguments: %+v", result)
	}
}

func TestTextContentToLLMDict(t *testing.T) {
	content := TextContent{
		BaseContent: BaseContent{CachePrompt: true},
		Text:        "hello",
	}

	blocks := content.ToLLMDict()
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0]["type"] != "text" || blocks[0]["text"] != "hello" {
		t.Fatalf("unexpected block: %+v", blocks[0])
	}
	if cache, ok := blocks[0]["cache_control"].(map[string]string); !ok || cache["type"] != "ephemeral" {
		t.Fatalf("expected ephemeral cache control, got %+v", blocks[0]["cache_control"])
	}
}

func TestImageContentToLLMDict(t *testing.T) {
	content := ImageContent{
		BaseContent: BaseContent{CachePrompt: true},
		ImageURLs:   []string{"https://example.com/a.png", "https://example.com/b.png"},
	}

	blocks := content.ToLLMDict()
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	last := blocks[1]
	if last["type"] != "image_url" {
		t.Fatalf("unexpected block type: %+v", last)
	}
	if _, ok := last["cache_control"]; !ok {
		t.Fatalf("expected cache control on last image block: %+v", last)
	}
}
