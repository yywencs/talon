package terminal_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	terminal "github.com/wen/opentalon/internal/tool/terminal"
	"github.com/wen/opentalon/internal/types"
)

const (
	blackboxPaneID       = "blackbox-pane-1"
	blackboxSecondPaneID = "blackbox-pane-2"
)

type blackboxBackend struct {
	mu             sync.Mutex
	baseWorkingDir string
	nextPID        int
	panes          map[string]*blackboxPaneState
}

type blackboxPaneState struct {
	screen     string
	workingDir string
	env        map[string]string
	pid        int
	pending    *blackboxPending
}

type blackboxPending struct {
	marker  string
	command string
}

func newBlackboxBackend(baseWorkingDir string) *blackboxBackend {
	return &blackboxBackend{
		baseWorkingDir: baseWorkingDir,
		nextPID:        3000,
		panes:          make(map[string]*blackboxPaneState),
	}
}

func (b *blackboxBackend) Initialize(ctx context.Context) error {
	return nil
}

func (b *blackboxBackend) Close(ctx context.Context) error {
	return nil
}

func (b *blackboxBackend) PrepareCommand(ctx context.Context, paneID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensurePaneLocked(paneID)
	return nil
}

func (b *blackboxBackend) CompleteCommand(ctx context.Context, paneID string) error {
	return nil
}

func (b *blackboxBackend) InvalidateCommand(ctx context.Context, paneID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.panes, paneID)
	return nil
}

func (b *blackboxBackend) ResetPane(ctx context.Context, paneID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.panes, paneID)
	return nil
}

func (b *blackboxBackend) SendKeys(ctx context.Context, paneID, text string, enter bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	pane := b.ensurePaneLocked(paneID)
	if wrappedCommand, marker, ok := unwrapWrappedCommand(text); ok {
		b.executeWrappedCommandLocked(pane, wrappedCommand, marker)
		return nil
	}

	if pane.pending != nil && text != "" {
		pane.screen += text
		if enter {
			pane.screen += "\n"
		}
	}
	return nil
}

func (b *blackboxBackend) ReadScreen(ctx context.Context, paneID string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	pane, ok := b.panes[paneID]
	if !ok {
		return "", fmt.Errorf("pane %q is not available", paneID)
	}
	return pane.screen, nil
}

func (b *blackboxBackend) ClearScreen(ctx context.Context, paneID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	pane, ok := b.panes[paneID]
	if !ok {
		return fmt.Errorf("pane %q is not available", paneID)
	}
	pane.screen = ""
	return nil
}

func (b *blackboxBackend) Interrupt(ctx context.Context, paneID string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	pane, ok := b.panes[paneID]
	if !ok || pane.pending == nil {
		return false, nil
	}
	b.appendCompletedOutputLocked(pane, "", pane.pending.marker, 130)
	pane.pending = nil
	return true, nil
}

func (b *blackboxBackend) IsRunning(ctx context.Context, paneID string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	pane, ok := b.panes[paneID]
	if !ok {
		return false, nil
	}
	return pane.pending != nil, nil
}

func (b *blackboxBackend) PanePID(ctx context.Context, paneID string) (*int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	pane, ok := b.panes[paneID]
	if !ok {
		return nil, nil
	}
	pid := pane.pid
	return &pid, nil
}

func (b *blackboxBackend) CurrentWorkingDir(ctx context.Context, paneID string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	pane, ok := b.panes[paneID]
	if !ok {
		return "", nil
	}
	return pane.workingDir, nil
}

func (b *blackboxBackend) ensurePaneLocked(paneID string) *blackboxPaneState {
	if pane, ok := b.panes[paneID]; ok {
		return pane
	}
	b.nextPID++
	pane := &blackboxPaneState{
		workingDir: b.baseWorkingDir,
		env:        make(map[string]string),
		pid:        b.nextPID,
	}
	b.panes[paneID] = pane
	return pane
}

func (b *blackboxBackend) executeWrappedCommandLocked(pane *blackboxPaneState, command, marker string) {
	switch {
	case command == "pwd":
		b.appendCompletedOutputLocked(pane, pane.workingDir+"\n", marker, 0)
	case strings.HasPrefix(command, "cd "):
		pane.workingDir = strings.TrimSpace(strings.TrimPrefix(command, "cd "))
		b.appendCompletedOutputLocked(pane, "", marker, 0)
	case strings.HasPrefix(command, "export "):
		envSpec := strings.TrimSpace(strings.TrimPrefix(command, "export "))
		key, value, ok := strings.Cut(envSpec, "=")
		if ok {
			pane.env[strings.TrimSpace(key)] = strings.Trim(value, `"'`)
		}
		b.appendCompletedOutputLocked(pane, "", marker, 0)
	case strings.HasPrefix(command, `printf '%s' "$`) && strings.HasSuffix(command, `"`):
		key := strings.TrimSuffix(strings.TrimPrefix(command, `printf '%s' "$`), `"`)
		b.appendCompletedOutputLocked(pane, pane.env[key], marker, 0)
	case command == "cat":
		pane.pending = &blackboxPending{
			marker:  marker,
			command: command,
		}
	case strings.HasPrefix(command, "echo "):
		b.appendCompletedOutputLocked(pane, strings.TrimPrefix(command, "echo ")+"\n", marker, 0)
	default:
		b.appendCompletedOutputLocked(pane, "", marker, 0)
	}
}

func (b *blackboxBackend) appendCompletedOutputLocked(pane *blackboxPaneState, output, marker string, exitCode int) {
	pane.screen += output
	pane.screen += "__OPENTALON_EXIT__:" + marker + ":" + strconv.Itoa(exitCode) + "\n"
}

func blackboxAction(paneID, command string) terminal.TerminalAction {
	return terminal.TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "terminal blackbox test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: command,
		PaneID:  paneID,
	}
}

func floatPtr(v float64) *float64 {
	return &v
}

func unwrapWrappedCommand(text string) (string, string, bool) {
	command, rest, found := strings.Cut(text, "; __opentalon_exit_code=$?;")
	if !found {
		return "", "", false
	}
	_, markerText, found := strings.Cut(rest, "__OPENTALON_EXIT__:")
	if !found {
		return "", "", false
	}
	marker, _, found := strings.Cut(markerText, ":%s")
	if !found {
		return "", "", false
	}
	return strings.TrimSpace(command), marker, true
}

func TestTerminalBlackbox_ValidationAndObservationContract(t *testing.T) {
	baseDir := t.TempDir()
	executor := terminal.NewExecutor(terminal.ExecutorConfig{
		WorkingDir: baseDir,
		Backend:    newBlackboxBackend(baseDir),
	})

	obs := executor.Execute(context.Background(), terminal.TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "missing pane id",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "pwd",
	})
	if obs == nil {
		t.Fatal("expected observation")
	}
	if obs.ExitCodeValue() != -1 {
		t.Fatalf("expected exit code -1, got %d", obs.ExitCodeValue())
	}
	if !strings.Contains(obs.OutputText(), "pane_id is empty") {
		t.Fatalf("expected pane_id validation error, got %q", obs.OutputText())
	}
}

func TestTerminalBlackbox_FixedPaneKeepsShellStateAcrossCommands(t *testing.T) {
	baseDir := t.TempDir()
	executor := terminal.NewExecutor(terminal.ExecutorConfig{
		WorkingDir: baseDir,
		Backend:    newBlackboxBackend(baseDir),
	})

	projectDir := baseDir + "/project"
	if obs := executor.Execute(context.Background(), blackboxAction(blackboxPaneID, "cd "+projectDir)); obs.ExitCodeValue() != 0 {
		t.Fatalf("cd failed: %q", obs.OutputText())
	}
	if obs := executor.Execute(context.Background(), blackboxAction(blackboxPaneID, "export TEST_BOX_VAR=world")); obs.ExitCodeValue() != 0 {
		t.Fatalf("export failed: %q", obs.OutputText())
	}

	pwdObs := executor.Execute(context.Background(), blackboxAction(blackboxPaneID, "pwd"))
	if pwdObs.ExitCodeValue() != 0 {
		t.Fatalf("pwd failed: %q", pwdObs.OutputText())
	}
	if pwdObs.OutputText() != projectDir+"\n" {
		t.Fatalf("pwd output = %q, want %q", pwdObs.OutputText(), projectDir+"\n")
	}
	if pwdObs.Metadata.WorkingDir != projectDir {
		t.Fatalf("metadata working_dir = %q, want %q", pwdObs.Metadata.WorkingDir, projectDir)
	}
	if pwdObs.Metadata.PaneID != blackboxPaneID {
		t.Fatalf("metadata pane_id = %q, want %q", pwdObs.Metadata.PaneID, blackboxPaneID)
	}
	if pwdObs.Metadata.PID == nil {
		t.Fatal("expected pid metadata")
	}

	envObs := executor.Execute(context.Background(), blackboxAction(blackboxPaneID, `printf '%s' "$TEST_BOX_VAR"`))
	if envObs.ExitCodeValue() != 0 {
		t.Fatalf("printf env failed: %q", envObs.OutputText())
	}
	if envObs.OutputText() != "world" {
		t.Fatalf("env output = %q, want %q", envObs.OutputText(), "world")
	}
}

func TestTerminalBlackbox_ResetOnlyAffectsCurrentPane(t *testing.T) {
	baseDir := t.TempDir()
	executor := terminal.NewExecutor(terminal.ExecutorConfig{
		WorkingDir: baseDir,
		Backend:    newBlackboxBackend(baseDir),
	})

	paneDir := baseDir + "/pane-1"
	if obs := executor.Execute(context.Background(), blackboxAction(blackboxPaneID, "cd "+paneDir)); obs.ExitCodeValue() != 0 {
		t.Fatalf("pane1 cd failed: %q", obs.OutputText())
	}
	if obs := executor.Execute(context.Background(), blackboxAction(blackboxPaneID, "export TEST_BOX_VAR=one")); obs.ExitCodeValue() != 0 {
		t.Fatalf("pane1 export failed: %q", obs.OutputText())
	}
	if obs := executor.Execute(context.Background(), blackboxAction(blackboxSecondPaneID, "export TEST_BOX_VAR=two")); obs.ExitCodeValue() != 0 {
		t.Fatalf("pane2 export failed: %q", obs.OutputText())
	}

	resetAction := blackboxAction(blackboxPaneID, "")
	resetAction.Reset = true
	resetObs := executor.Execute(context.Background(), resetAction)
	if resetObs.ExitCodeValue() != 0 {
		t.Fatalf("reset failed: %q", resetObs.OutputText())
	}
	if !strings.Contains(resetObs.OutputText(), "终端会话已重置") {
		t.Fatalf("reset output = %q, want reset message", resetObs.OutputText())
	}

	pane1Pwd := executor.Execute(context.Background(), blackboxAction(blackboxPaneID, "pwd"))
	if pane1Pwd.OutputText() != baseDir+"\n" {
		t.Fatalf("pane1 pwd after reset = %q, want %q", pane1Pwd.OutputText(), baseDir+"\n")
	}
	pane1Env := executor.Execute(context.Background(), blackboxAction(blackboxPaneID, `printf '%s' "$TEST_BOX_VAR"`))
	if pane1Env.OutputText() != "" {
		t.Fatalf("pane1 env after reset = %q, want empty", pane1Env.OutputText())
	}

	pane2Env := executor.Execute(context.Background(), blackboxAction(blackboxSecondPaneID, `printf '%s' "$TEST_BOX_VAR"`))
	if pane2Env.OutputText() != "two" {
		t.Fatalf("pane2 env after pane1 reset = %q, want %q", pane2Env.OutputText(), "two")
	}
}

func TestTerminalBlackbox_InteractiveFlowCanContinueAndInterrupt(t *testing.T) {
	baseDir := t.TempDir()
	executor := terminal.NewExecutor(terminal.ExecutorConfig{
		WorkingDir:     baseDir,
		DefaultTimeout: 10 * time.Millisecond,
		Backend:        newBlackboxBackend(baseDir),
	})

	startAction := blackboxAction(blackboxPaneID, "cat")
	startAction.Timeout = floatPtr(0.01)
	startObs := executor.Execute(context.Background(), startAction)
	if startObs.ExitCodeValue() != -1 || !startObs.Timeout {
		t.Fatalf("expected interactive start to timeout, got exit=%d timeout=%v output=%q", startObs.ExitCodeValue(), startObs.Timeout, startObs.OutputText())
	}

	inputAction := blackboxAction(blackboxPaneID, "hello")
	inputAction.IsInput = true
	inputAction.Timeout = floatPtr(0.01)
	inputObs := executor.Execute(context.Background(), inputAction)
	if inputObs.ExitCodeValue() != -1 || !inputObs.Timeout {
		t.Fatalf("expected interactive input to keep running, got exit=%d timeout=%v output=%q", inputObs.ExitCodeValue(), inputObs.Timeout, inputObs.OutputText())
	}
	if !strings.Contains(inputObs.OutputText(), "hello") {
		t.Fatalf("expected interactive output to contain echoed input, got %q", inputObs.OutputText())
	}

	interruptAction := blackboxAction(blackboxPaneID, "C-c")
	interruptAction.IsInput = true
	interruptObs := executor.Execute(context.Background(), interruptAction)
	if interruptObs.ExitCodeValue() != 130 {
		t.Fatalf("expected interrupt exit code 130, got %d, output=%q", interruptObs.ExitCodeValue(), interruptObs.OutputText())
	}

	invalidInputAction := blackboxAction(blackboxPaneID, "after")
	invalidInputAction.IsInput = true
	invalidInputObs := executor.Execute(context.Background(), invalidInputAction)
	if invalidInputObs.ExitCodeValue() != -1 {
		t.Fatalf("expected invalid input after interrupt to fail, got %d", invalidInputObs.ExitCodeValue())
	}
	if !strings.Contains(invalidInputObs.OutputText(), "is_input 需要当前存在可继续交互的运行中命令") {
		t.Fatalf("unexpected invalid input error: %q", invalidInputObs.OutputText())
	}

	finalObs := executor.Execute(context.Background(), blackboxAction(blackboxPaneID, "echo done"))
	if finalObs.ExitCodeValue() != 0 {
		t.Fatalf("expected pane to remain usable after interrupt, got %q", finalObs.OutputText())
	}
	if finalObs.OutputText() != "done\n" {
		t.Fatalf("final output = %q, want %q", finalObs.OutputText(), "done\n")
	}
}
