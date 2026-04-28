package terminal

import (
	"context"
	"fmt"
	"testing"
)

func TestTmuxBackendCommandLifecycleKeepsFixedPaneBinding(t *testing.T) {
	runner := &fakeTmuxRunner{
		lookPath: map[string]string{
			"tmux": "/usr/bin/tmux",
			"bash": "/bin/bash",
		},
	}
	var nextPaneID int
	runner.runFunc = func(args ...string) (string, error) {
		switch args[0] {
		case "new-session":
			nextPaneID = 1
			return "%1\n", nil
		case "new-window":
			nextPaneID++
			return fmt.Sprintf("%%%d\n", nextPaneID), nil
		default:
			return "", nil
		}
	}

	backend := NewTmuxBackend("/tmp")
	backend.runner = runner

	if err := backend.PrepareCommand(context.Background(), defaultTestPaneID); err != nil {
		t.Fatalf("prepare first command: %v", err)
	}
	firstPane := backend.paneBindings[defaultTestPaneID]
	if firstPane == nil {
		t.Fatal("expected fixed pane binding after first prepare")
	}

	if err := backend.CompleteCommand(context.Background(), defaultTestPaneID); err != nil {
		t.Fatalf("complete first command: %v", err)
	}
	if backend.paneBindings[defaultTestPaneID] == nil {
		t.Fatal("expected fixed pane binding to remain after complete")
	}

	if err := backend.PrepareCommand(context.Background(), defaultTestPaneID); err != nil {
		t.Fatalf("prepare second command: %v", err)
	}
	if backend.paneBindings[defaultTestPaneID].PaneID != firstPane.PaneID {
		t.Fatalf("expected fixed pane %q to be reused, got %q", firstPane.PaneID, backend.paneBindings[defaultTestPaneID].PaneID)
	}

	if err := backend.PrepareCommand(context.Background(), secondTestPaneID); err != nil {
		t.Fatalf("prepare second pane command: %v", err)
	}
	if backend.paneBindings[secondTestPaneID] == nil {
		t.Fatal("expected second fixed pane binding")
	}
	if backend.paneBindings[secondTestPaneID].PaneID == firstPane.PaneID {
		t.Fatal("expected different pane_id to get a different fixed pane")
	}
}

func TestTmuxBackendResetPaneRemovesBindingAndCreatesFreshPane(t *testing.T) {
	runner := &fakeTmuxRunner{
		lookPath: map[string]string{
			"tmux": "/usr/bin/tmux",
			"bash": "/bin/bash",
		},
	}
	var nextPaneID int
	var killTargets []string
	runner.runFunc = func(args ...string) (string, error) {
		switch args[0] {
		case "new-session":
			nextPaneID = 1
			return "%1\n", nil
		case "new-window":
			nextPaneID++
			return fmt.Sprintf("%%%d\n", nextPaneID), nil
		case "kill-pane":
			if len(args) >= 3 {
				killTargets = append(killTargets, args[2])
			}
			return "", nil
		default:
			return "", nil
		}
	}

	backend := NewTmuxBackend("/tmp")
	backend.runner = runner

	if err := backend.PrepareCommand(context.Background(), defaultTestPaneID); err != nil {
		t.Fatalf("prepare first command: %v", err)
	}
	firstPane := backend.paneBindings[defaultTestPaneID]
	if firstPane == nil {
		t.Fatal("expected fixed pane binding after first prepare")
	}

	if err := backend.ResetPane(context.Background(), defaultTestPaneID); err != nil {
		t.Fatalf("reset pane: %v", err)
	}
	if backend.paneBindings[defaultTestPaneID] != nil {
		t.Fatal("expected fixed pane binding to be cleared after reset")
	}
	if len(killTargets) != 1 || killTargets[0] != firstPane.PaneID {
		t.Fatalf("expected reset to kill pane %q, got %#v", firstPane.PaneID, killTargets)
	}

	if err := backend.PrepareCommand(context.Background(), defaultTestPaneID); err != nil {
		t.Fatalf("prepare command after reset: %v", err)
	}
	if backend.paneBindings[defaultTestPaneID] == nil {
		t.Fatal("expected fixed pane binding after reset prepare")
	}
	if backend.paneBindings[defaultTestPaneID].PaneID == firstPane.PaneID {
		t.Fatalf("expected a fresh pane after reset, got reused pane %q", firstPane.PaneID)
	}
}

func TestTmuxBackendMetadataDoesNotFallbackToOtherPane(t *testing.T) {
	runner := &fakeTmuxRunner{
		lookPath: map[string]string{
			"tmux": "/usr/bin/tmux",
			"bash": "/bin/bash",
		},
	}
	runner.runFunc = func(args ...string) (string, error) {
		switch args[0] {
		case "new-session":
			return "%1\n", nil
		case "display-message":
			return "/tmp\n", nil
		default:
			return "", nil
		}
	}

	backend := NewTmuxBackend("/tmp")
	backend.runner = runner

	if err := backend.PrepareCommand(context.Background(), defaultTestPaneID); err != nil {
		t.Fatalf("prepare bound pane: %v", err)
	}

	workingDir, err := backend.CurrentWorkingDir(context.Background(), secondTestPaneID)
	if err != nil {
		t.Fatalf("unexpected working dir error for unbound pane: %v", err)
	}
	if workingDir != "" {
		t.Fatalf("expected empty working dir for unbound pane, got %q", workingDir)
	}

	pid, err := backend.PanePID(context.Background(), secondTestPaneID)
	if err != nil {
		t.Fatalf("unexpected pid error for unbound pane: %v", err)
	}
	if pid != nil {
		t.Fatalf("expected nil pid for unbound pane, got %v", *pid)
	}

	if _, err := backend.ReadScreen(context.Background(), secondTestPaneID); err == nil {
		t.Fatal("expected read screen on unbound pane to fail")
	}
}
