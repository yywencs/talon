package agent

import (
	"strings"
	"testing"
	"time"

	terminalpkg "github.com/wen/opentalon/internal/tool/terminal"
	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/utils"
)

type fakeEvent struct {
	types.BaseEvent
	kind types.EventKind
	name string
}

func (e *fakeEvent) Kind() types.EventKind { return e.kind }
func (e *fakeEvent) Name() string          { return e.name }

func TestPromptBuilderBuildMessages(t *testing.T) {
	eventLog := types.NewEventLog()
	eventLog.Append(&types.MessageEvent{
		BaseEvent: types.BaseEvent{Source: types.SourceUser},
		Role:      types.RoleUser,
		Content:   []types.Content{types.TextContent{Text: "请看看目录"}},
	})
	eventLog.Append(&types.ActionEvent{
		BaseEvent:  types.BaseEvent{Source: types.SourceAgent},
		ActionID:   "2",
		ActionType: types.ActionRun,
	})
	eventLog.Append(&types.ObservationEvent{
		BaseEvent:   types.BaseEvent{Source: types.SourceEnvironment},
		ActionID:    "2",
		ToolName:    "bash",
		Observation: terminalpkg.NewTerminalObservation("ls -la", "", nil, false, 0, "file-a\nfile-b"),
	})

	builder := NewPromptBuilder()
	messages := builder.BuildMessages(&types.SessionState{
		Events: eventLog,
	}, "system prompt", "user example prompt")

	if len(messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(messages))
	}
	if messages[0].Role != "system" || utils.FlattenTextContent(messages[0].Content) != "system prompt" {
		t.Fatalf("unexpected system message: %+v", messages[0])
	}
	if messages[1].Role != "user" || utils.FlattenTextContent(messages[1].Content) != "user example prompt" {
		t.Fatalf("unexpected user example message: %+v", messages[1])
	}
	if messages[2].Role != types.RoleUser || utils.FlattenTextContent(messages[2].Content) != "请看看目录" {
		t.Fatalf("unexpected message event: %+v", messages[2])
	}
	if messages[3].Role != types.RoleTool || utils.FlattenTextContent(messages[3].Content) != "file-a\nfile-b" {
		t.Fatalf("unexpected observation event: %+v", messages[3])
	}
	assertCachePrompt(t, messages[0].Content[len(messages[0].Content)-1], true)
	assertCachePrompt(t, messages[1].Content[len(messages[1].Content)-1], true)
}

func TestPromptBuilderBuildMessages_OnlySystemWhenNoExamplesOrEvents(t *testing.T) {
	builder := NewPromptBuilder()
	messages := builder.BuildMessages(&types.SessionState{}, "system prompt", "")

	if len(messages) != 1 {
		t.Fatalf("expected only system message, got %d", len(messages))
	}
	if messages[0].Role != types.RoleSystem {
		t.Fatalf("expected system role, got %q", messages[0].Role)
	}
	if got := utils.FlattenTextContent(messages[0].Content); got != "system prompt" {
		t.Fatalf("unexpected system prompt content: %q", got)
	}
	assertCachePrompt(t, messages[0].Content[len(messages[0].Content)-1], true)
}

func TestPromptBuilderBuildMessages_FiltersEventsWithoutPayload(t *testing.T) {
	eventLog := types.NewEventLog()
	eventLog.Append(&types.ActionEvent{
		BaseEvent:  types.BaseEvent{Source: types.SourceAgent},
		ActionID:   "1",
		ActionType: types.ActionRun,
	})
	eventLog.Append(&types.MessageEvent{
		BaseEvent: types.BaseEvent{Source: types.SourceUser},
		Role:      types.RoleUser,
	})
	eventLog.Append(&fakeEvent{
		BaseEvent: types.BaseEvent{
			ID:        "ignored",
			Timestamp: time.Now(),
			Source:    types.SourceAgent,
		},
		kind: "custom",
		name: "custom",
	})

	builder := NewPromptBuilder()
	messages := builder.BuildMessages(&types.SessionState{
		Events: eventLog,
	}, "system prompt", "")

	if len(messages) != 1 {
		t.Fatalf("expected only system message after filtering empty/unsupported events, got %d", len(messages))
	}
}

func TestPromptBuilderBuildMessages_PreservesAssistantToolCallAndReasoning(t *testing.T) {
	eventLog := types.NewEventLog()
	eventLog.Append(&types.ActionEvent{
		BaseEvent:        types.BaseEvent{Source: types.SourceAgent},
		ActionID:         "1",
		ActionType:       types.ActionRun,
		ReasoningContent: "先读取文件再决定下一步。",
		ToolCall: &types.MessageToolCall{
			ID:        "call_1",
			Name:      "read",
			Arguments: `{"path":"README.md"}`,
		},
	})

	builder := NewPromptBuilder()
	messages := builder.BuildMessages(&types.SessionState{
		Events: eventLog,
	}, "system prompt", "")

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	assistant := messages[1]
	if assistant.Role != types.RoleAssistant {
		t.Fatalf("expected assistant role, got %q", assistant.Role)
	}
	if assistant.ReasoningContent != "先读取文件再决定下一步。" {
		t.Fatalf("unexpected reasoning content: %q", assistant.ReasoningContent)
	}
	if len(assistant.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(assistant.ToolCalls))
	}
	if assistant.ToolCalls[0].Name != "read" || assistant.ToolCalls[0].Arguments != `{"path":"README.md"}` {
		t.Fatalf("unexpected tool call: %+v", assistant.ToolCalls[0])
	}
}

func TestApplyEphemeralCacheControls_MarksStablePrefixAndRollingWindow(t *testing.T) {
	messages := make([]types.Message, 45)
	for i := range messages {
		messages[i] = types.Message{
			Role: types.RoleUser,
			Content: []types.Content{
				types.TextContent{Text: strings.Repeat("m", i+1)},
			},
		}
	}

	applyEphemeralCacheControls(messages, true)

	expectedMarked := map[int]bool{
		0:  true,
		1:  true,
		24: true,
		44: true,
	}
	for idx := range messages {
		got := contentHasCachePrompt(messages[idx].Content[len(messages[idx].Content)-1])
		if got != expectedMarked[idx] {
			t.Fatalf("message %d cache marker = %v, want %v", idx, got, expectedMarked[idx])
		}
	}
}

func TestApplyEphemeralCacheControls_SkipsAlreadyMarkedPrefix(t *testing.T) {
	messages := []types.Message{
		{
			Role: types.RoleSystem,
			Content: []types.Content{
				types.TextContent{BaseContent: types.BaseContent{CachePrompt: true}, Text: "system"},
			},
		},
		{
			Role: types.RoleUser,
			Content: []types.Content{
				types.TextContent{BaseContent: types.BaseContent{CachePrompt: true}, Text: "examples"},
			},
		},
		{
			Role: types.RoleUser,
			Content: []types.Content{
				types.TextContent{Text: "latest"},
			},
		},
	}

	applyEphemeralCacheControls(messages, true)

	assertCachePrompt(t, messages[0].Content[0], true)
	assertCachePrompt(t, messages[1].Content[0], true)
	assertCachePrompt(t, messages[2].Content[0], true)
}

func TestEventToMessage_ObservationWithoutOutputUsesDefaultText(t *testing.T) {
	msg, ok := eventToMessage(&types.ObservationEvent{
		BaseEvent:  types.BaseEvent{Source: types.SourceEnvironment},
		ActionID:   "1",
		ToolName:   "bash",
		ToolCallID: "call_1",
	})
	if !ok {
		t.Fatal("expected observation event to be converted")
	}
	if msg.Role != types.RoleTool {
		t.Fatalf("expected tool role, got %q", msg.Role)
	}
	if msg.Name != "bash" || msg.ToolCallID != "call_1" {
		t.Fatalf("unexpected tool metadata: %+v", msg)
	}
	if got := utils.FlattenTextContent(msg.Content); got != "Command executed successfully with no output." {
		t.Fatalf("unexpected default observation text: %q", got)
	}
}

func TestEventToMessage_ObservationRejectionOverridesContent(t *testing.T) {
	msg, ok := eventToMessage(&types.ObservationEvent{
		BaseEvent:       types.BaseEvent{Source: types.SourceEnvironment},
		ActionID:        "1",
		ToolName:        "bash",
		RejectionReason: "requires approval",
		Observation:     terminalpkg.NewTerminalObservation("rm -rf /", "", nil, false, 1, "should be ignored"),
	})
	if !ok {
		t.Fatal("expected observation event to be converted")
	}
	if got := utils.FlattenTextContent(msg.Content); got != "Action rejected: requires approval" {
		t.Fatalf("unexpected rejection text: %q", got)
	}
}

func TestEventToMessage_UnsupportedEventReturnsFalse(t *testing.T) {
	_, ok := eventToMessage(&fakeEvent{
		BaseEvent: types.BaseEvent{
			ID:        "evt_1",
			Timestamp: time.Now(),
			Source:    types.SourceAgent,
		},
		kind: types.KindMessage,
		name: "fake",
	})
	if ok {
		t.Fatal("expected unsupported event implementation to be ignored")
	}
}

func TestEventToJSON(t *testing.T) {
	data, ok := eventToJSON(&types.MessageEvent{
		BaseEvent: types.BaseEvent{Source: types.SourceUser},
		Role:      types.RoleUser,
		Content:   []types.Content{types.TextContent{Text: "你好"}},
	})
	if !ok {
		t.Fatal("expected message event to be serialized")
	}
	if !strings.Contains(data, `"role":"user"`) || !strings.Contains(data, `"text":"你好"`) {
		t.Fatalf("unexpected event json: %s", data)
	}
}

func TestEventToJSON_UnsupportedEventReturnsFalse(t *testing.T) {
	_, ok := eventToJSON(&fakeEvent{
		BaseEvent: types.BaseEvent{
			ID:        "evt_2",
			Timestamp: time.Now(),
			Source:    types.SourceAgent,
		},
		kind: "custom",
		name: "custom",
	})
	if ok {
		t.Fatal("expected unsupported event json conversion to fail")
	}
}

func assertCachePrompt(t *testing.T, content types.Content, want bool) {
	t.Helper()
	if got := contentHasCachePrompt(content); got != want {
		t.Fatalf("cache prompt = %v, want %v, content=%#v", got, want, content)
	}
}

func contentHasCachePrompt(content types.Content) bool {
	switch c := content.(type) {
	case types.TextContent:
		return c.CachePrompt
	case *types.TextContent:
		return c != nil && c.CachePrompt
	case types.ImageContent:
		return c.CachePrompt
	case *types.ImageContent:
		return c != nil && c.CachePrompt
	default:
		return false
	}
}
