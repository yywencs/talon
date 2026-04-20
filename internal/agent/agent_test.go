package agent

import (
	"testing"

	"github.com/wen/opentalon/internal/types"
)

// TestActionBatchTruncateAtFinish 验证 finish 之后的动作会被截断。
func TestActionBatchTruncateAtFinish(t *testing.T) {
	batch := &toolCallBatch{
		calls: []types.MessageToolCall{
			{Name: "bash"},
			{Name: "finish"},
			{Name: "bash"},
		},
	}

	batch.truncateAtFinish()

	if !batch.finished {
		t.Fatal("expected batch to be marked as finished")
	}
	if len(batch.calls) != 2 {
		t.Fatalf("expected actions after finish to be truncated, got %d", len(batch.calls))
	}
	if batch.calls[1].Name != "finish" {
		t.Fatalf("expected finish action to be kept as last event, got %+v", batch.calls[1])
	}
}

// TestActionBatchTruncateAtFinish_NoFinish 验证不存在 finish 时不截断。
func TestActionBatchTruncateAtFinish_NoFinish(t *testing.T) {
	batch := &toolCallBatch{
		calls: []types.MessageToolCall{
			{Name: "bash"},
			{Name: "read"},
		},
	}

	batch.truncateAtFinish()

	if batch.finished {
		t.Fatal("expected batch to remain unfinished")
	}
	if len(batch.calls) != 2 {
		t.Fatalf("expected no truncation, got %d events", len(batch.calls))
	}
}

// TestResponseToTurnResult_MoveReasoningToAction 验证工具调用前的推理内容会挂到 ActionEvent，而不是单独消息。
func TestResponseToTurnResult_MoveReasoningToAction(t *testing.T) {
	agent := &Agent{}
	resp := &ChatResponse{
		Message: types.Message{
			Role:             types.RoleAssistant,
			ReasoningContent: "用户想查看当前目录内容，应该调用 ls。",
			ToolCalls: []types.MessageToolCall{
				{
					ID:        "call_1",
					Name:      "bash",
					Arguments: `{"command":"ls -la"}`,
				},
			},
		},
	}

	result, err := agent.responseToTurnResult(resp)
	if err != nil {
		t.Fatalf("responseToTurnResult failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Message != nil {
		t.Fatalf("expected no standalone assistant message, got %+v", result.Message)
	}
	if result.ActionReasoningContent != "用户想查看当前目录内容，应该调用 ls。" {
		t.Fatalf("unexpected action reasoning content: %q", result.ActionReasoningContent)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.ToolCalls))
	}
}
