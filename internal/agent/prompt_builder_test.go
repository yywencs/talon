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

	if len(messages) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(messages))
	}
	if messages[0].Role != "system" || messages[0].Content != "system prompt" {
		t.Fatalf("unexpected system message: %+v", messages[0])
	}
	if messages[1].Role != "user" || messages[1].Content != "user example prompt" {
		t.Fatalf("unexpected user example message: %+v", messages[1])
	}
	if messages[2].Role != "user" || messages[2].Content != "请看看目录" {
		t.Fatalf("unexpected user message: %+v", messages[2])
	}
	if messages[3].Role != "assistant" {
		t.Fatalf("expected assistant role for command action, got %q", messages[3].Role)
	}
	if messages[4].Role != "user" {
		t.Fatalf("expected user role for observation, got %q", messages[4].Role)
	}
	if messages[0].CacheControl["type"] != "ephemeral" {
		t.Fatalf("expected system message to be cacheable, got %+v", messages[0].CacheControl)
	}
	if messages[1].CacheControl["type"] != "ephemeral" {
		t.Fatalf("expected example message to be cacheable, got %+v", messages[1].CacheControl)
	}
	if messages[4].CacheControl["type"] != "ephemeral" {
		t.Fatalf("expected latest message to be cacheable, got %+v", messages[4].CacheControl)
	}
}
