package agent

import (
	"testing"

	toolpkg "github.com/wen/opentalon/internal/tool"
	"github.com/wen/opentalon/internal/types"
)

func TestPromptBuilderBuildMessages(t *testing.T) {
	builder := NewPromptBuilder()
	messages := builder.BuildMessages(&types.State{
		History: []types.Event{
			&types.MessageAction{
				BaseEvent: types.BaseEvent{Source: types.SourceUser},
				Content:   "请看看目录",
			},
			&types.ActionEvent{
				BaseEvent:  types.BaseEvent{Source: types.SourceAgent},
				ActionID:   "2",
				ActionType: types.ActionRun,
				Action:     &toolpkg.TerminalAction{Command: "ls -la"},
			},
			&types.ObservationEvent{
				BaseEvent:   types.BaseEvent{Source: types.SourceEnvironment},
				ActionID:    "2",
				ToolName:    "bash",
				Observation: toolpkg.NewTerminalObservation("ls -la", "", nil, false, 0, "file-a\nfile-b"),
			},
		},
	}, "system prompt", "user example prompt")

	if len(messages) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(messages))
	}
	if messages[0].Role != "system" || types.FlattenTextContent(messages[0].Content) != "system prompt" {
		t.Fatalf("unexpected system message: %+v", messages[0])
	}
	if messages[1].Role != "user" || types.FlattenTextContent(messages[1].Content) != "user example prompt" {
		t.Fatalf("unexpected user example message: %+v", messages[1])
	}
	if messages[2].Role != "user" || types.FlattenTextContent(messages[2].Content) != "请看看目录" {
		t.Fatalf("unexpected user message: %+v", messages[2])
	}
	if messages[3].Role != "assistant" {
		t.Fatalf("expected assistant role for command action, got %q", messages[3].Role)
	}
	if messages[4].Role != "user" {
		t.Fatalf("expected user role for observation, got %q", messages[4].Role)
	}
}
