package terminal

import (
	"context"
	"errors"
	"fmt"
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

	interruptOK     bool
	running         bool
	panePID         *int
	workingDir      string
	screen          string
	sendCalls       []fakeSendCall
	initialized     bool
	initializeCalls int
	closeCalls      int
	onInitialize    func()
	onClose         func()
	onSend          func(text string, enter bool)
	onInterrupt     func()
}

type fakeSendCall struct {
	text  string
	enter bool
}

func (b *fakeBackend) Initialize(ctx context.Context) error {
	if b.initializeErr != nil {
		return b.initializeErr
	}

	b.mu.Lock()
	if b.initialized {
		b.mu.Unlock()
		return nil
	}
	b.initialized = true
	b.initializeCalls++
	onInitialize := b.onInitialize
	b.mu.Unlock()

	if onInitialize != nil {
		onInitialize()
	}
	return nil
}

func (b *fakeBackend) Close(ctx context.Context) error {
	if b.closeErr != nil {
		return b.closeErr
	}

	b.mu.Lock()
	if !b.initialized {
		b.mu.Unlock()
		return nil
	}
	b.initialized = false
	b.closeCalls++
	onClose := b.onClose
	b.mu.Unlock()

	if onClose != nil {
		onClose()
	}
	return nil
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
		action  TerminalAction
		wantErr string
	}{
		{
			name: "empty command",
			action: TerminalAction{
				Command: "",
			},
			wantErr: "command is empty",
		},
		{
			name: "invalid timeout",
			action: TerminalAction{
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
	err := validateAction(&TerminalAction{IsInput: true})
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

func TestTerminalActionActionFields(t *testing.T) {
	typ := reflect.TypeFor[TerminalAction]()
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
		t.Fatal("working_dir should not appear in TerminalAction action fields")
	}
	if _, ok := fields["timeout_secs"]; ok {
		t.Fatal("timeout_secs should not appear in TerminalAction action fields")
	}
}

func TestNewTerminalObservation(t *testing.T) {
	pid := 123
	obs := NewTerminalObservation("echo hi", "/tmp", &pid, true, -1, "命令执行超时")

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
	if obs.OutputText() != "命令执行超时" {
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

func TestBuildTerminalErrorMessageIncludesHint(t *testing.T) {
	msg := BuildTerminalErrorMessage(NewTerminalStateError(
		"is_input 需要当前存在可继续交互的运行中命令",
		"请先启动一个命令，再通过 is_input=true 继续交互",
	))
	if !strings.Contains(msg, "is_input 需要当前存在可继续交互的运行中命令") {
		t.Fatalf("expected primary error message, got %q", msg)
	}
	if !strings.Contains(msg, "提示: 请先启动一个命令，再通过 is_input=true 继续交互") {
		t.Fatalf("expected actionable hint, got %q", msg)
	}
}

func TestTruncateIfNeeded(t *testing.T) {
	output := strings.Repeat("a", maxOutputSize+10)
	got, truncated := truncateIfNeeded(output)
	if !truncated {
		t.Fatal("expected output to be truncated")
	}
	if !strings.HasSuffix(got, "[输出已截断]") {
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

	obs := executor.Execute(context.Background(), TerminalAction{
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

	obs := executor.Execute(context.Background(), TerminalAction{
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

func TestExecutorResetClosesSessionWithoutReinitializing(t *testing.T) {
	baseDir := t.TempDir()
	backend := &fakeBackend{
		initialized: true,
	}
	executor := NewExecutor(ExecutorConfig{
		WorkingDir: baseDir,
		Backend:    backend,
	})

	obs := executor.Execute(context.Background(), TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Reset: true,
	})
	if obs.ExitCodeValue() != 0 {
		t.Fatalf("expected exit code 0, got %d", obs.ExitCodeValue())
	}
	if obs.OutputText() != "终端会话已重置" {
		t.Fatalf("expected reset output, got %q", obs.OutputText())
	}
	if obs.Metadata.WorkingDir != baseDir {
		t.Fatalf("expected working dir %q, got %q", baseDir, obs.Metadata.WorkingDir)
	}
	if backend.closeCalls != 1 {
		t.Fatalf("expected 1 close call, got %d", backend.closeCalls)
	}
	if backend.initializeCalls != 0 {
		t.Fatalf("expected reset without command to avoid reinitialize, got %d", backend.initializeCalls)
	}
}

func TestExecutorResetClearsSessionStateBeforeNextCommand(t *testing.T) {
	baseDir := t.TempDir()

	backend := &fakeBackend{
		workingDir: baseDir,
	}
	var sessionID int
	envValue := ""
	backend.onInitialize = func() {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		sessionID++
		backend.panePID = intPtr(1000 + sessionID)
		backend.workingDir = baseDir
		backend.screen = ""
		envValue = ""
	}
	backend.onClose = func() {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		backend.panePID = nil
		backend.workingDir = baseDir
		backend.screen = ""
		envValue = ""
	}
	backend.onSend = func(text string, enter bool) {
		command := unwrapCommand(text)
		if command == "" {
			return
		}
		marker := extractMarkerFromWrappedCommand(text)

		backend.mu.Lock()
		defer backend.mu.Unlock()

		switch command {
		case "cd /tmp/changed":
			backend.workingDir = "/tmp/changed"
		case `export FOO=bar`:
			envValue = "bar"
		case "pwd":
			backend.screen += backend.workingDir + "\n"
		case `printf '<%s>\n' "$FOO"`:
			backend.screen += "<" + envValue + ">\n"
		}
		backend.screen += commandExitPrefix + marker + ":0\n"
	}

	executor := NewExecutor(ExecutorConfig{
		WorkingDir: baseDir,
		Backend:    backend,
	})

	run := func(action TerminalAction) *TerminalObservation {
		return executor.Execute(context.Background(), action)
	}
	toolMeta := types.ToolMetadata{
		Summary:      "test",
		SecurityRisk: types.SecurityRisk_HIGH,
	}

	if obs := run(TerminalAction{ToolMetadata: toolMeta, Command: "cd /tmp/changed"}); obs.ExitCodeValue() != 0 {
		t.Fatalf("expected cd to succeed, got %q", obs.OutputText())
	}
	if obs := run(TerminalAction{ToolMetadata: toolMeta, Command: `export FOO=bar`}); obs.ExitCodeValue() != 0 {
		t.Fatalf("expected export to succeed, got %q", obs.OutputText())
	}

	resetObs := run(TerminalAction{ToolMetadata: toolMeta, Reset: true})
	if resetObs.ExitCodeValue() != 0 {
		t.Fatalf("expected reset to succeed, got %q", resetObs.OutputText())
	}

	pwdObs := run(TerminalAction{ToolMetadata: toolMeta, Command: "pwd"})
	if pwdObs.ExitCodeValue() != 0 {
		t.Fatalf("expected pwd to succeed, got %q", pwdObs.OutputText())
	}
	if pwdObs.OutputText() != baseDir+"\n" {
		t.Fatalf("expected reset working dir %q, got %q", baseDir+"\\n", pwdObs.OutputText())
	}
	if pwdObs.Metadata.PID == nil || *pwdObs.Metadata.PID != 1002 {
		t.Fatalf("expected new session pid 1002, got %#v", pwdObs.Metadata.PID)
	}

	envObs := run(TerminalAction{ToolMetadata: toolMeta, Command: `printf '<%s>\n' "$FOO"`})
	if envObs.ExitCodeValue() != 0 {
		t.Fatalf("expected env read to succeed, got %q", envObs.OutputText())
	}
	if envObs.OutputText() != "<>\n" {
		t.Fatalf("expected reset env to be empty, got %q", envObs.OutputText())
	}
	if backend.closeCalls != 1 {
		t.Fatalf("expected exactly one close call, got %d", backend.closeCalls)
	}
}

func TestExecutorResetClearsPendingInputSession(t *testing.T) {
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
	backend.onClose = func() {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		backend.screen = ""
	}

	executor := NewExecutor(ExecutorConfig{
		DefaultTimeout: 10 * time.Millisecond,
		Backend:        backend,
	})

	firstObs := executor.Execute(context.Background(), TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "cat",
	})
	if !firstObs.Timeout {
		t.Fatal("expected first observation to time out")
	}

	resetObs := executor.Execute(context.Background(), TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Reset: true,
	})
	if resetObs.ExitCodeValue() != 0 {
		t.Fatalf("expected reset to succeed, got %q", resetObs.OutputText())
	}

	secondObs := executor.Execute(context.Background(), TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		IsInput: true,
		Command: "done",
	})
	if !strings.Contains(secondObs.OutputText(), "is_input 需要当前存在可继续交互的运行中命令") {
		t.Fatalf("expected pending command to be cleared, got %q", secondObs.OutputText())
	}
}

func TestExecutorResetDoesNotAllowContinuingInputInSameCall(t *testing.T) {
	backend := &fakeBackend{}
	executor := NewExecutor(ExecutorConfig{
		Backend: backend,
	})

	obs := executor.Execute(context.Background(), TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Reset:   true,
		IsInput: true,
		Command: "hello",
	})
	if !strings.Contains(obs.OutputText(), "发送输入前请先启动新命令") {
		t.Fatalf("expected reset input error, got %q", obs.OutputText())
	}
	if len(backend.sendCalls) != 0 {
		t.Fatalf("expected no input to be forwarded during reset, got %d send calls", len(backend.sendCalls))
	}
}

func TestExecutorRequiresRunningCommandForInput(t *testing.T) {
	executor := NewExecutor(ExecutorConfig{
		Backend: &fakeBackend{},
	})

	obs := executor.Execute(context.Background(), TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		IsInput: true,
		Command: "hello",
	})
	if !strings.Contains(obs.OutputText(), "is_input 需要当前存在可继续交互的运行中命令") {
		t.Fatalf("expected input mode error, got %q", obs.OutputText())
	}
}

func TestExecutorEmptyInputWithoutPendingCommandReturnsEmptyOutput(t *testing.T) {
	executor := NewExecutor(ExecutorConfig{
		Backend: &fakeBackend{},
	})

	obs := executor.Execute(context.Background(), TerminalAction{
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
			backend.running = true
			return
		}
		if text == "done" && marker != "" {
			backend.running = false
			backend.screen += "finished\n" + commandExitPrefix + marker + ":0\n"
		}
	}

	executor := NewExecutor(ExecutorConfig{
		DefaultTimeout: 10 * time.Millisecond,
		Backend:        backend,
	})

	firstObs := executor.Execute(context.Background(), TerminalAction{
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

	secondObs := executor.Execute(context.Background(), TerminalAction{
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
			backend.mu.Lock()
			backend.running = true
			marker = extractMarkerFromWrappedCommand(text)
			backend.mu.Unlock()
		}
	}

	executor := NewExecutor(ExecutorConfig{
		DefaultTimeout: 10 * time.Millisecond,
		Backend:        backend,
	})

	firstObs := executor.Execute(context.Background(), TerminalAction{
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
	backend.running = false
	backend.screen += "more output\n" + commandExitPrefix + marker + ":0\n"
	backend.mu.Unlock()

	secondObs := executor.Execute(context.Background(), TerminalAction{
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

func TestExecutorEmptyInputDoesNotRepeatConsumedOutput(t *testing.T) {
	backend := &fakeBackend{
		running: true,
	}
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

	firstObs := executor.Execute(context.Background(), TerminalAction{
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
	backend.screen += "chunk1\n"
	backend.mu.Unlock()

	secondObs := executor.Execute(context.Background(), TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		IsInput: true,
		Command: "",
	})
	if !strings.Contains(secondObs.OutputText(), "chunk1\n") {
		t.Fatalf("expected first pull to contain chunk1, got %q", secondObs.OutputText())
	}

	backend.mu.Lock()
	backend.screen += "chunk2\n"
	backend.mu.Unlock()

	thirdObs := executor.Execute(context.Background(), TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		IsInput: true,
		Command: "",
	})
	if strings.Contains(thirdObs.OutputText(), "chunk1\n") {
		t.Fatalf("expected consumed output not to repeat, got %q", thirdObs.OutputText())
	}
	if !strings.Contains(thirdObs.OutputText(), "chunk2\n") {
		t.Fatalf("expected second pull to contain chunk2, got %q", thirdObs.OutputText())
	}

	backend.mu.Lock()
	backend.screen += commandExitPrefix + marker + ":0\n"
	backend.running = false
	backend.mu.Unlock()

	finalObs := executor.Execute(context.Background(), TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		IsInput: true,
		Command: "",
	})
	if finalObs.ExitCodeValue() != 0 {
		t.Fatalf("expected final exit code 0, got %d", finalObs.ExitCodeValue())
	}
}

func TestExecutorReturnsErrorAndClearsStalePendingBeforeNextCommand(t *testing.T) {
	backend := &fakeBackend{}
	backend.onSend = func(text string, enter bool) {
		command := unwrapCommand(text)
		if command == "" {
			return
		}
		marker := extractMarkerFromWrappedCommand(text)

		backend.mu.Lock()
		defer backend.mu.Unlock()

		if command == "echo fresh" {
			backend.screen += "fresh\n" + commandExitPrefix + marker + ":0\n"
			return
		}
		backend.running = false
	}

	executor := NewExecutor(ExecutorConfig{
		DefaultTimeout: 10 * time.Millisecond,
		Backend:        backend,
	})

	firstObs := executor.Execute(context.Background(), TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "sleep 1",
	})
	if !firstObs.Timeout {
		t.Fatal("expected first observation to time out")
	}

	secondObs := executor.Execute(context.Background(), TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "echo fresh",
	})
	if secondObs.ExitCodeValue() != -1 {
		t.Fatalf("expected stale pending error, got %d", secondObs.ExitCodeValue())
	}
	if !strings.Contains(secondObs.OutputText(), "由于终端中已没有运行中的前台命令，pending 执行状态已被清理") {
		t.Fatalf("expected stale pending error, got %q", secondObs.OutputText())
	}

	thirdObs := executor.Execute(context.Background(), TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "echo fresh",
	})
	if thirdObs.ExitCodeValue() != 0 {
		t.Fatalf("expected retry after stale pending cleanup to succeed, got %q", thirdObs.OutputText())
	}
	if thirdObs.OutputText() != "fresh\n" {
		t.Fatalf("expected fresh output on retry, got %q", thirdObs.OutputText())
	}
}

func TestExecutorReturnsErrorAndClearsStalePendingBeforeRejectingInput(t *testing.T) {
	backend := &fakeBackend{}
	backend.onSend = func(text string, enter bool) {
		if strings.Contains(text, commandExitPrefix) {
			return
		}
		t.Fatalf("unexpected input forwarded after stale pending cleanup: %q", text)
	}

	executor := NewExecutor(ExecutorConfig{
		DefaultTimeout: 10 * time.Millisecond,
		Backend:        backend,
	})

	firstObs := executor.Execute(context.Background(), TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "sleep 1",
	})
	if !firstObs.Timeout {
		t.Fatal("expected first observation to time out")
	}

	secondObs := executor.Execute(context.Background(), TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		IsInput: true,
		Command: "done",
	})
	if !strings.Contains(secondObs.OutputText(), "由于终端中已没有运行中的前台命令，pending 执行状态已被清理") {
		t.Fatalf("expected stale pending error before rejecting input, got %q", secondObs.OutputText())
	}
	if !strings.Contains(secondObs.OutputText(), "提示: 之前的交互命令已经退出") {
		t.Fatalf("expected stale pending hint, got %q", secondObs.OutputText())
	}

	thirdObs := executor.Execute(context.Background(), TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		IsInput: true,
		Command: "done",
	})
	if !strings.Contains(thirdObs.OutputText(), "is_input 需要当前存在可继续交互的运行中命令") {
		t.Fatalf("expected later input to fall back to running command error, got %q", thirdObs.OutputText())
	}
}

func TestExecutorInterruptMapsToBackendInterrupt(t *testing.T) {
	backend := &fakeBackend{
		interruptOK: true,
	}
	var marker string
	backend.onSend = func(text string, enter bool) {
		if strings.Contains(text, commandExitPrefix) {
			backend.mu.Lock()
			backend.running = true
			marker = extractMarkerFromWrappedCommand(text)
			backend.mu.Unlock()
		}
	}
	backend.onInterrupt = func() {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		backend.running = false
		backend.screen += "interrupted\n" + commandExitPrefix + marker + ":130\n"
	}

	executor := NewExecutor(ExecutorConfig{
		DefaultTimeout: 10 * time.Millisecond,
		Backend:        backend,
	})

	firstObs := executor.Execute(context.Background(), TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "sleep 1",
	})
	if !firstObs.Timeout {
		t.Fatal("expected first observation to time out")
	}

	secondObs := executor.Execute(context.Background(), TerminalAction{
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

func TestExecutorReadErrorClearsPendingState(t *testing.T) {
	backend := &fakeBackend{
		running: true,
	}
	executor := NewExecutor(ExecutorConfig{
		DefaultTimeout: 10 * time.Millisecond,
		Backend:        backend,
	})

	firstObs := executor.Execute(context.Background(), TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "sleep 1",
	})
	if !firstObs.Timeout {
		t.Fatal("expected first observation to time out")
	}

	backend.readErr = errors.New("read failed")
	secondObs := executor.Execute(context.Background(), TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		IsInput: true,
		Command: "",
	})
	if !strings.Contains(secondObs.OutputText(), "读取终端屏幕失败") {
		t.Fatalf("expected read error, got %q", secondObs.OutputText())
	}
	if !strings.Contains(secondObs.OutputText(), "提示: 可以先重试一次，或继续用 is_input=true 拉取输出") {
		t.Fatalf("expected read error hint, got %q", secondObs.OutputText())
	}

	backend.readErr = nil
	thirdObs := executor.Execute(context.Background(), TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		IsInput: true,
		Command: "done",
	})
	if !strings.Contains(thirdObs.OutputText(), "is_input 需要当前存在可继续交互的运行中命令") {
		t.Fatalf("expected pending state to be cleared after read error, got %q", thirdObs.OutputText())
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

	obs := BashExecutor(context.Background(), TerminalAction{
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
			return "@1 %1\n", nil
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

func TestTmuxBackendCommandLifecycleReusesIdlePane(t *testing.T) {
	runner := &fakeTmuxRunner{
		lookPath: map[string]string{
			"tmux": "/usr/bin/tmux",
			"bash": "/bin/bash",
		},
	}
	var nextWindowID int
	runner.runFunc = func(args ...string) (string, error) {
		switch args[0] {
		case "new-session":
			nextWindowID = 1
			return "@1 %1\n", nil
		case "new-window":
			nextWindowID++
			return fmt.Sprintf("@%d %%%d\n", nextWindowID, nextWindowID), nil
		default:
			return "", nil
		}
	}

	backend := NewTmuxBackend("/tmp")
	backend.runner = runner

	if err := backend.PrepareCommand(context.Background()); err != nil {
		t.Fatalf("prepare first command: %v", err)
	}
	firstPane := backend.activePane
	if firstPane == nil {
		t.Fatal("expected active pane after first prepare")
	}
	if err := backend.CompleteCommand(context.Background()); err != nil {
		t.Fatalf("complete first command: %v", err)
	}
	if backend.activePane != nil {
		t.Fatal("expected active pane to be cleared after complete")
	}
	if len(backend.idlePanes) != 1 {
		t.Fatalf("expected 1 idle pane, got %d", len(backend.idlePanes))
	}

	if err := backend.PrepareCommand(context.Background()); err != nil {
		t.Fatalf("prepare second command: %v", err)
	}
	if backend.activePane == nil {
		t.Fatal("expected active pane after second prepare")
	}
	if backend.activePane.PaneID != firstPane.PaneID {
		t.Fatalf("expected reused pane %q, got %q", firstPane.PaneID, backend.activePane.PaneID)
	}
}

func TestTmuxBackendCompleteCommandEvictsOldestIdlePaneWhenQueueFull(t *testing.T) {
	runner := &fakeTmuxRunner{
		lookPath: map[string]string{
			"tmux": "/usr/bin/tmux",
			"bash": "/bin/bash",
		},
	}
	var nextWindowID int
	var killTargets []string
	runner.runFunc = func(args ...string) (string, error) {
		switch args[0] {
		case "new-session":
			nextWindowID = 1
			return "@1 %1\n", nil
		case "new-window":
			nextWindowID++
			return fmt.Sprintf("@%d %%%d\n", nextWindowID, nextWindowID), nil
		case "kill-window":
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
	backend.maxIdlePanes = 2
	backend.initialized = true
	backend.idlePanes = []*tmuxPaneHandle{
		{WindowID: "@1", PaneID: "%1"},
		{WindowID: "@2", PaneID: "%2"},
	}
	backend.activePane = &tmuxPaneHandle{WindowID: "@3", PaneID: "%3"}

	if err := backend.CompleteCommand(context.Background()); err != nil {
		t.Fatalf("complete command: %v", err)
	}

	if len(backend.idlePanes) != 2 {
		t.Fatalf("expected idle queue size 2, got %d", len(backend.idlePanes))
	}
	if len(killTargets) != 1 {
		t.Fatalf("expected exactly one evicted window, got %d", len(killTargets))
	}
	if killTargets[0] != "@1" {
		t.Fatalf("expected oldest window @1 to be evicted, got %q", killTargets[0])
	}
	if backend.idlePanes[0].WindowID != "@2" || backend.idlePanes[1].WindowID != "@3" {
		t.Fatalf("expected queue to retain newer panes, got %#v", backend.idlePanes)
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

func unwrapCommand(text string) string {
	command, _, found := strings.Cut(text, "; __opentalon_exit_code=$?;")
	if !found {
		return ""
	}
	return strings.TrimSpace(command)
}
