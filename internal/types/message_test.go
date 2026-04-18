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
