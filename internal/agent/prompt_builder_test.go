package agent

import (
	"testing"

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
			&types.CmdRunAction{
				BaseEvent: types.BaseEvent{Source: types.SourceAgent},
				Command:   "ls -la",
				Thought:   "先看文件结构",
			},
			&types.CmdOutputObservation{
				BaseEvent: types.BaseEvent{Cause: 2},
				Content:   "file-a\nfile-b",
				ExitCode:  0,
			},
		},
	}, "system prompt", "user example prompt")

	if len(messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(messages))
	}
	if messages[0].Role != "system" || messages[0].Content != "system prompt" {
		t.Fatalf("unexpected system message: %+v", messages[0])
	}
	if messages[1].Role != "user" || messages[1].Content != "请看看目录" {
		t.Fatalf("unexpected user message: %+v", messages[1])
	}
	if messages[2].Role != "assistant" {
		t.Fatalf("expected assistant role for command action, got %q", messages[2].Role)
	}
	if messages[3].Role != "user" {
		t.Fatalf("expected user role for observation, got %q", messages[3].Role)
	}
}
