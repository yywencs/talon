package terminal

import (
	"context"
	"errors"
	"testing"

	"github.com/wen/opentalon/internal/types"
)

func TestExecutorNotifiesBackendFailureOnInitializeError(t *testing.T) {
	initializeErr := errors.New("initialize failed")
	backend := &fakeBackend{initializeErr: initializeErr}

	var notified error
	executor := NewExecutor(ExecutorConfig{
		Backend: backend,
		OnBackendFailure: func(ctx context.Context, cause error) {
			_ = ctx
			notified = cause
		},
	})

	obs := executor.Execute(context.Background(), TerminalAction{
		ToolMetadata: types.ToolMetadata{Summary: "test", SecurityRisk: types.SecurityRisk_HIGH},
		Command:      "pwd",
		PaneID:       defaultTestPaneID,
	})

	if obs.ExitCodeValue() != -1 {
		t.Fatalf("exit code = %d, want -1", obs.ExitCodeValue())
	}
	if !errors.Is(notified, initializeErr) {
		t.Fatalf("notified error = %v, want %v", notified, initializeErr)
	}
}

func TestExecutorNotifiesBackendFailureOnRuntimeReadError(t *testing.T) {
	readErr := errors.New("tmux session disappeared")
	backend := &fakeBackend{readErr: readErr}

	var notified error
	executor := NewExecutor(ExecutorConfig{
		Backend: backend,
		OnBackendFailure: func(ctx context.Context, cause error) {
			_ = ctx
			notified = cause
		},
	})

	obs := executor.Execute(context.Background(), TerminalAction{
		ToolMetadata: types.ToolMetadata{Summary: "test", SecurityRisk: types.SecurityRisk_HIGH},
		Command:      "pwd",
		PaneID:       defaultTestPaneID,
	})

	if obs.ExitCodeValue() != -1 {
		t.Fatalf("exit code = %d, want -1", obs.ExitCodeValue())
	}
	if !errors.Is(notified, readErr) {
		t.Fatalf("notified error = %v, want %v", notified, readErr)
	}
}
