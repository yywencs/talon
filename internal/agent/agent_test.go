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
