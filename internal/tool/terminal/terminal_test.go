package terminal

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wen/opentalon/internal/types"
)

type fakeBackend struct {
	mu sync.Mutex

	initializeErr error
	closeErr      error
	sendErr       error
	readErr       error
	interruptErr  error
	clearErr      error

	interruptOK bool
	running     bool
	panePID     *int
	workingDir  string
	screen      string
	sendCalls   []fakeSendCall
	onSend      func(text string, enter bool)
	onInterrupt func()
}

type fakeSendCall struct {
	text  string
	enter bool
}

func (b *fakeBackend) Initialize(ctx context.Context) error {
	return b.initializeErr
}

func (b *fakeBackend) Close(ctx context.Context) error {
	return b.closeErr
}

func (b *fakeBackend) SendKeys(ctx context.Context, text string, enter bool) error {
	if b.sendErr != nil {
		return b.sendErr
	}

	b.mu.Lock()
	b.sendCalls = append(b.sendCalls, fakeSendCall{text: text, enter: enter})
	onSend := b.onSend
	b.mu.Unlock()

	if onSend != nil {
		onSend(text, enter)
	}
	return nil
}

func (b *fakeBackend) ReadScreen(ctx context.Context) (string, error) {
	if b.readErr != nil {
		return "", b.readErr
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	return b.screen, nil
}

func (b *fakeBackend) ClearScreen(ctx context.Context) error {
	if b.clearErr != nil {
		return b.clearErr
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.screen = ""
	return nil
}

func (b *fakeBackend) Interrupt(ctx context.Context) (bool, error) {
	if b.interruptErr != nil {
		return false, b.interruptErr
	}
	if b.onInterrupt != nil {
		b.onInterrupt()
	}
	return b.interruptOK, nil
}

func (b *fakeBackend) IsRunning(ctx context.Context) (bool, error) {
	return b.running, nil
}

func (b *fakeBackend) PanePID(ctx context.Context) (*int, error) {
	return b.panePID, nil
}

func (b *fakeBackend) CurrentWorkingDir(ctx context.Context) (string, error) {
	return b.workingDir, nil
}

type fakeTmuxRunner struct {
	mu sync.Mutex

	lookPath map[string]string
	runFunc  func(args ...string) (string, error)
	calls    [][]string
}

func (r *fakeTmuxRunner) Run(ctx context.Context, args ...string) (string, error) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string(nil), args...))
	runFunc := r.runFunc
	r.mu.Unlock()
	if runFunc == nil {
		return "", nil
	}
	return runFunc(args...)
}

func (r *fakeTmuxRunner) LookPath(file string) (string, error) {
	if path, ok := r.lookPath[file]; ok {
		return path, nil
	}
	return "", os.ErrNotExist
}

func testFloatPtr(v float64) *float64 {
	return &v
}

func TestValidateAction(t *testing.T) {
	tests := []struct {
		name    string
		action  BashTool
		wantErr string
	}{
		{
			name: "empty command",
			action: BashTool{
				Command: "",
			},
			wantErr: "command is empty",
		},
		{
			name: "invalid timeout",
			action: BashTool{
				Command: "echo hi",
				Timeout: testFloatPtr(301),
			},
			wantErr: "timeout out of range",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAction(&tt.action)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestValidateAction_InputModeAllowsEmptyCommand(t *testing.T) {
	err := validateAction(&BashTool{IsInput: true})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestValidateWorkingDir(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := tmpDir + "/file.txt"
	if err := os.WriteFile(tmpFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	tests := []struct {
		name       string
		workingDir string
		wantErr    string
	}{
		{
			name:       "missing working dir",
			workingDir: tmpDir + "/missing",
			wantErr:    "working_dir does not exist",
		},
		{
			name:       "working dir is file",
			workingDir: tmpFile,
			wantErr:    "working_dir is not a directory",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateWorkingDir(tt.workingDir)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestBashToolActionFields(t *testing.T) {
	typ := reflect.TypeFor[BashTool]()
	fields := make(map[string]struct{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := field.Tag.Get("json")
		if tag == "" || tag == ",inline" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		fields[name] = struct{}{}
	}

	for _, name := range []string{"command", "is_input", "timeout", "reset"} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("expected field %q to exist", name)
		}
	}
	if _, ok := fields["working_dir"]; ok {
		t.Fatal("working_dir should not appear in BashTool action fields")
	}
	if _, ok := fields["timeout_secs"]; ok {
		t.Fatal("timeout_secs should not appear in BashTool action fields")
	}
}

func TestNewTerminalObservation(t *testing.T) {
	pid := 123
	obs := NewTerminalObservation("echo hi", "/tmp", &pid, true, -1, "command timed out")

	if !obs.ErrorStatus {
		t.Fatal("expected ErrorStatus=true")
	}
	if !obs.Timeout {
		t.Fatal("expected Timeout=true")
	}
	if obs.ExitCode == nil || *obs.ExitCode != -1 {
		t.Fatalf("expected exit code -1, got %#v", obs.ExitCode)
	}
	if obs.Metadata.PID == nil || *obs.Metadata.PID != pid {
		t.Fatalf("expected pid %d, got %#v", pid, obs.Metadata.PID)
	}
	if obs.Metadata.WorkingDir != "/tmp" {
		t.Fatalf("expected working dir /tmp, got %q", obs.Metadata.WorkingDir)
	}
	if obs.OutputText() != "command timed out" {
		t.Fatalf("unexpected output text: %q", obs.OutputText())
	}
}

func TestAuditHelpers(t *testing.T) {
	if got := auditCommandName("  echo hello  "); got != "echo" {
		t.Fatalf("expected command name echo, got %q", got)
	}

	hash1 := auditCommandHash("echo hello")
	hash2 := auditCommandHash("echo hello")
	if hash1 == "" {
		t.Fatal("expected non-empty hash")
	}
	if hash1 != hash2 {
		t.Fatalf("expected stable hash, got %q and %q", hash1, hash2)
	}
}

func TestTruncateIfNeeded(t *testing.T) {
	output := strings.Repeat("a", maxOutputSize+10)
	got, truncated := truncateIfNeeded(output)
	if !truncated {
		t.Fatal("expected output to be truncated")
	}
	if !strings.HasSuffix(got, "[output truncated]") {
		t.Fatalf("expected truncation suffix, got %q", got[len(got)-20:])
	}
}

func TestExecutorUsesWorkingDir(t *testing.T) {
	pid := 4242
	backend := &fakeBackend{
		panePID:    &pid,
		workingDir: "/tmp",
	}
	backend.onSend = func(text string, enter bool) {
		marker := extractMarkerFromWrappedCommand(text)
		if marker == "" {
			return
		}
		backend.mu.Lock()
		defer backend.mu.Unlock()
		backend.screen += "/tmp\n" + commandExitPrefix + marker + ":0\n"
	}

	executor := NewExecutor(ExecutorConfig{
		WorkingDir: "/tmp",
		Backend:    backend,
	})

	obs := executor.Execute(context.Background(), BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "pwd",
	})

	if obs.ExitCodeValue() != 0 {
		t.Fatalf("expected exit code 0, got %d", obs.ExitCodeValue())
	}
	if obs.Metadata.WorkingDir != "/tmp" {
		t.Fatalf("expected working dir /tmp, got %q", obs.Metadata.WorkingDir)
	}
	if obs.Metadata.PID == nil || *obs.Metadata.PID != pid {
		t.Fatalf("expected pid %d, got %#v", pid, obs.Metadata.PID)
	}
	if !strings.Contains(obs.OutputText(), "/tmp") {
		t.Fatalf("expected output to contain /tmp, got %q", obs.OutputText())
	}
}

func TestExecutorRejectsInvalidWorkingDir(t *testing.T) {
	executor := NewExecutor(ExecutorConfig{
		WorkingDir: "/nonexistent/path/that/does/not/exist",
		Backend:    &fakeBackend{},
	})

	obs := executor.Execute(context.Background(), BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "echo hello",
	})

	if obs.ExitCodeValue() != -1 {
		t.Fatalf("expected exit code -1, got %d", obs.ExitCodeValue())
	}
	if !strings.Contains(obs.OutputText(), "working_dir does not exist") {
		t.Fatalf("expected invalid working_dir error, got %q", obs.OutputText())
	}
}

func TestExecutorResetRemainsUnimplemented(t *testing.T) {
	executor := NewExecutor(ExecutorConfig{
		Backend: &fakeBackend{},
	})

	obs := executor.Execute(context.Background(), BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Reset: true,
	})
	if !strings.Contains(obs.OutputText(), "reset is not implemented yet") {
		t.Fatalf("expected reset unsupported message, got %q", obs.OutputText())
	}
}

func TestExecutorRequiresRunningCommandForInput(t *testing.T) {
	executor := NewExecutor(ExecutorConfig{
		Backend: &fakeBackend{},
	})

	obs := executor.Execute(context.Background(), BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		IsInput: true,
		Command: "hello",
	})
	if !strings.Contains(obs.OutputText(), "is_input requires a running command") {
		t.Fatalf("expected input mode error, got %q", obs.OutputText())
	}
}

func TestExecutorEmptyInputWithoutPendingCommandReturnsEmptyOutput(t *testing.T) {
	executor := NewExecutor(ExecutorConfig{
		Backend: &fakeBackend{},
	})

	obs := executor.Execute(context.Background(), BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		IsInput: true,
		Command: "",
	})
	if obs.ExitCodeValue() != 0 {
		t.Fatalf("expected exit code 0, got %d", obs.ExitCodeValue())
	}
	if obs.OutputText() != "" {
		t.Fatalf("expected empty output, got %q", obs.OutputText())
	}
}

func TestExecutorInputCanContinueTimedOutCommand(t *testing.T) {
	backend := &fakeBackend{}
	var marker string
	backend.onSend = func(text string, enter bool) {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		if strings.Contains(text, commandExitPrefix) {
			marker = extractMarkerFromWrappedCommand(text)
			return
		}
		if text == "done" && marker != "" {
			backend.screen += "finished\n" + commandExitPrefix + marker + ":0\n"
		}
	}

	executor := NewExecutor(ExecutorConfig{
		DefaultTimeout: 10 * time.Millisecond,
		Backend:        backend,
	})

	firstObs := executor.Execute(context.Background(), BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "cat",
	})
	if !firstObs.Timeout {
		t.Fatal("expected first observation to time out")
	}
	if firstObs.ExitCodeValue() != -1 {
		t.Fatalf("expected exit code -1, got %d", firstObs.ExitCodeValue())
	}

	secondObs := executor.Execute(context.Background(), BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		IsInput: true,
		Command: "done",
	})
	if secondObs.ExitCodeValue() != 0 {
		t.Fatalf("expected exit code 0, got %d", secondObs.ExitCodeValue())
	}
	if secondObs.OutputText() != "finished\n" {
		t.Fatalf("expected finished output, got %q", secondObs.OutputText())
	}
}

func TestExecutorEmptyInputPullsNewOutput(t *testing.T) {
	backend := &fakeBackend{}
	var marker string
	backend.onSend = func(text string, enter bool) {
		if strings.Contains(text, commandExitPrefix) {
			marker = extractMarkerFromWrappedCommand(text)
		}
	}

	executor := NewExecutor(ExecutorConfig{
		DefaultTimeout: 10 * time.Millisecond,
		Backend:        backend,
	})

	firstObs := executor.Execute(context.Background(), BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "sleep 1",
	})
	if !firstObs.Timeout {
		t.Fatal("expected first observation to time out")
	}

	backend.mu.Lock()
	backend.screen += "more output\n" + commandExitPrefix + marker + ":0\n"
	backend.mu.Unlock()

	secondObs := executor.Execute(context.Background(), BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		IsInput: true,
		Command: "",
	})
	if secondObs.ExitCodeValue() != 0 {
		t.Fatalf("expected exit code 0, got %d", secondObs.ExitCodeValue())
	}
	if secondObs.OutputText() != "more output\n" {
		t.Fatalf("expected pulled output, got %q", secondObs.OutputText())
	}
}

func TestExecutorInterruptMapsToBackendInterrupt(t *testing.T) {
	backend := &fakeBackend{
		interruptOK: true,
	}
	var marker string
	backend.onSend = func(text string, enter bool) {
		if strings.Contains(text, commandExitPrefix) {
			marker = extractMarkerFromWrappedCommand(text)
		}
	}
	backend.onInterrupt = func() {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		backend.screen += "interrupted\n" + commandExitPrefix + marker + ":130\n"
	}

	executor := NewExecutor(ExecutorConfig{
		DefaultTimeout: 10 * time.Millisecond,
		Backend:        backend,
	})

	firstObs := executor.Execute(context.Background(), BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "sleep 1",
	})
	if !firstObs.Timeout {
		t.Fatal("expected first observation to time out")
	}

	secondObs := executor.Execute(context.Background(), BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		IsInput: true,
		Command: "C-c",
	})
	if secondObs.ExitCodeValue() != 130 {
		t.Fatalf("expected exit code 130, got %d", secondObs.ExitCodeValue())
	}
	if secondObs.OutputText() != "interrupted\n" {
		t.Fatalf("expected interrupted output, got %q", secondObs.OutputText())
	}
}

func TestBashExecutorUsesDefaultExecutor(t *testing.T) {
	backend := &fakeBackend{}
	backend.onSend = func(text string, enter bool) {
		marker := extractMarkerFromWrappedCommand(text)
		if marker == "" {
			return
		}
		backend.mu.Lock()
		defer backend.mu.Unlock()
		backend.screen += "hello\n" + commandExitPrefix + marker + ":0\n"
	}

	previousExecutor := defaultExecutor
	defaultExecutor = NewExecutor(ExecutorConfig{
		Backend: backend,
	})
	t.Cleanup(func() {
		defaultExecutor = previousExecutor
	})

	obs := BashExecutor(context.Background(), BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "echo hello",
	})

	if obs.ExitCodeValue() != 0 {
		t.Fatalf("expected exit code 0, got %d", obs.ExitCodeValue())
	}
	if obs.OutputText() != "hello\n" {
		t.Fatalf("expected output hello\\n, got %q", obs.OutputText())
	}
}

func TestTmuxBackendInitializeRequiresTmuxBinary(t *testing.T) {
	backend := &TmuxBackend{
		runner: &fakeTmuxRunner{
			lookPath: map[string]string{
				"bash": "/bin/bash",
			},
		},
		session: "test-session",
	}

	err := backend.Initialize(context.Background())
	if err == nil {
		t.Fatal("expected missing tmux error")
	}
	if !strings.Contains(err.Error(), "tmux is not available") {
		t.Fatalf("expected tmux missing error, got %v", err)
	}
}

func TestTmuxBackendReadScreenMarksUninitializedWhenSessionIsMissing(t *testing.T) {
	runner := &fakeTmuxRunner{
		lookPath: map[string]string{
			"tmux": "/usr/bin/tmux",
			"bash": "/bin/bash",
		},
	}
	missingSessionErr := errors.New("tmux command failed")
	runner.runFunc = func(args ...string) (string, error) {
		switch {
		case len(args) >= 1 && args[0] == "new-session":
			return "", nil
		case len(args) >= 1 && args[0] == "set-option":
			return "", nil
		case len(args) >= 1 && args[0] == "send-keys":
			return "", nil
		case len(args) >= 1 && args[0] == "clear-history":
			return "", nil
		case len(args) >= 1 && args[0] == "capture-pane":
			return "can't find session: test-session", missingSessionErr
		default:
			return "", nil
		}
	}

	backend := &TmuxBackend{
		runner:  runner,
		session: "test-session",
	}

	if err := backend.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize backend: %v", err)
	}
	if !backend.initialized {
		t.Fatal("expected backend to be initialized")
	}

	_, err := backend.ReadScreen(context.Background())
	if err == nil {
		t.Fatal("expected read screen error")
	}
	if backend.initialized {
		t.Fatal("expected backend to reset initialized state after missing session")
	}
}

func TestPTYBackendReturnsNotImplemented(t *testing.T) {
	backend := NewPTYBackend("/tmp")
	if err := backend.Initialize(context.Background()); err == nil {
		t.Fatal("expected PTY initialize error")
	}
}

func extractMarkerFromWrappedCommand(text string) string {
	_, rest, found := strings.Cut(text, commandExitPrefix)
	if !found {
		return ""
	}
	marker, _, found := strings.Cut(rest, ":%s")
	if !found {
		return ""
	}
	return marker
}
