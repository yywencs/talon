package agent

import (
	"testing"

	toolpkg "github.com/wen/opentalon/internal/tool"
	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/utils"
)

func TestPromptBuilderBuildMessages(t *testing.T) {
	eventLog := types.NewEventLog()
	eventLog.Append(&types.MessageEvent{
		BaseEvent: types.BaseEvent{Source: types.SourceUser},
		LLMMessage: types.Message{
			Role:    types.RoleUser,
			Content: []types.Content{types.TextContent{Text: "请看看目录"}},
		},
	})
	eventLog.Append(&types.ActionEvent{
		BaseEvent:  types.BaseEvent{Source: types.SourceAgent},
		ActionID:   "2",
		ActionType: types.ActionRun,
		Action:     &toolpkg.TerminalAction{Command: "ls -la"},
	})
	eventLog.Append(&types.ObservationEvent{
		BaseEvent:   types.BaseEvent{Source: types.SourceEnvironment},
		ActionID:    "2",
		ToolName:    "bash",
		Observation: toolpkg.NewTerminalObservation("ls -la", "", nil, false, 0, "file-a\nfile-b"),
	})

	builder := NewPromptBuilder()
	messages := builder.BuildMessages(&types.SessionState{
		Events: eventLog,
	}, "system prompt", "user example prompt")

	if len(messages) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(messages))
	}
	if messages[0].Role != "system" || utils.FlattenTextContent(messages[0].Content) != "system prompt" {
		t.Fatalf("unexpected system message: %+v", messages[0])
	}
	if messages[1].Role != "user" || utils.FlattenTextContent(messages[1].Content) != "user example prompt" {
		t.Fatalf("unexpected user example message: %+v", messages[1])
	}
	if messages[2].Role != "user" {
		t.Fatalf("expected user role for MessageAction JSON, got %q", messages[2].Role)
	}
	if messages[3].Role != "user" {
		t.Fatalf("expected user role for ActionEvent JSON, got %q", messages[3].Role)
	}
	if messages[4].Role != "user" {
		t.Fatalf("expected user role for ObservationEvent JSON, got %q", messages[4].Role)
	}
}
