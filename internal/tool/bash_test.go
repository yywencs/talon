package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	sandboxpkg "github.com/wen/opentalon/internal/sandbox"
	terminalpkg "github.com/wen/opentalon/internal/tool/terminal"
	"github.com/wen/opentalon/internal/types"
)

type fakeToolBackend struct {
	mu            sync.Mutex
	screen        string
	workingDir    string
	initializeErr error
	readErr       error
	prepareErr    error
	closeCount    int
	sentPaneIDs   []string
}

func (b *fakeToolBackend) Initialize(ctx context.Context) error {
	_ = ctx
	return b.initializeErr
}

func (b *fakeToolBackend) Close(ctx context.Context) error {
	_ = ctx
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closeCount++
	return nil
}

func (b *fakeToolBackend) SendKeys(ctx context.Context, paneID, text string, enter bool) error {
	_ = ctx
	_ = enter

	b.mu.Lock()
	defer b.mu.Unlock()
	b.sentPaneIDs = append(b.sentPaneIDs, paneID)

	marker := extractToolTestMarker(text)
	if marker == "" {
		return nil
	}
	b.screen += "ok\n__OPENTALON_EXIT__:" + marker + ":0\n"
	return nil
}

func (b *fakeToolBackend) ReadScreen(ctx context.Context, paneID string) (string, error) {
	_ = ctx
	_ = paneID
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.readErr != nil {
		return "", b.readErr
	}
	return b.screen, nil
}

func (b *fakeToolBackend) ClearScreen(ctx context.Context, paneID string) error {
	_ = ctx
	_ = paneID
	b.mu.Lock()
	defer b.mu.Unlock()
	b.screen = ""
	return nil
}

func (b *fakeToolBackend) Interrupt(ctx context.Context, paneID string) (bool, error) {
	_ = ctx
	_ = paneID
	return true, nil
}

func (b *fakeToolBackend) IsRunning(ctx context.Context, paneID string) (bool, error) {
	_ = ctx
	_ = paneID
	return false, nil
}

func (b *fakeToolBackend) PrepareCommand(ctx context.Context, paneID string) error {
	_ = ctx
	_ = paneID
	return b.prepareErr
}

func (b *fakeToolBackend) CompleteCommand(ctx context.Context, paneID string) error {
	_ = ctx
	_ = paneID
	return nil
}

func (b *fakeToolBackend) InvalidateCommand(ctx context.Context, paneID string) error {
	_ = ctx
	_ = paneID
	return nil
}

func (b *fakeToolBackend) ResetPane(ctx context.Context, paneID string) error {
	_ = ctx
	_ = paneID
	b.mu.Lock()
	defer b.mu.Unlock()
	b.screen = ""
	return nil
}

func (b *fakeToolBackend) PanePID(ctx context.Context, paneID string) (*int, error) {
	_ = ctx
	_ = paneID
	pid := 123
	return &pid, nil
}

func (b *fakeToolBackend) CurrentWorkingDir(ctx context.Context, paneID string) (string, error) {
	_ = ctx
	_ = paneID
	return b.workingDir, nil
}

func extractToolTestMarker(text string) string {
	const prefix = "__OPENTALON_EXIT__:"
	start := strings.Index(text, prefix)
	if start < 0 {
		return ""
	}
	start += len(prefix)
	end := strings.Index(text[start:], ":")
	if end < 0 {
		return ""
	}
	return text[start : start+end]
}

func installBashToolTestHooks(t *testing.T, workingDir string, factory func(runtimeRoute string, workingDir string) terminalpkg.TerminalBackend) {
	t.Helper()
	installBashToolBundleTestHooks(t, workingDir, func(runtimeRoute string, workingDir string) bashBackendBundle {
		return bashBackendBundle{backend: factory(runtimeRoute, workingDir)}
	})
}

func installBashToolBundleTestHooks(t *testing.T, workingDir string, factory func(runtimeRoute string, workingDir string) bashBackendBundle) {
	t.Helper()

	prevResolve := resolveBashWorkingDir
	prevHostFactory := newHostBashBackendBundle
	prevSandboxFactory := newSandboxBashBackendBundle
	prevRegistry := bashSessionExecutors
	resolveBashWorkingDir = func() string {
		return workingDir
	}
	newHostBashBackendBundle = func(ctx context.Context, wd string) bashBackendBundle {
		_ = ctx
		return factory(bashRuntimeLabelHost, wd)
	}
	newSandboxBashBackendBundle = func(ctx context.Context, wd string) bashBackendBundle {
		_ = ctx
		return factory(bashRuntimeLabelSandbox, wd)
	}
	t.Cleanup(func() {
		resolveBashWorkingDir = prevResolve
		newHostBashBackendBundle = prevHostFactory
		newSandboxBashBackendBundle = prevSandboxFactory
		bashSessionExecutors = prevRegistry
	})
	bashSessionExecutors = newBashSessionRegistry()
}

func TestBashRegisteredToolUsesSandboxBackendByDefault(t *testing.T) {
	workingDir := t.TempDir()
	backend := &fakeToolBackend{workingDir: workingDir}
	var gotRoute string

	installBashToolTestHooks(t, workingDir, func(runtimeRoute string, wd string) terminalpkg.TerminalBackend {
		gotRoute = runtimeRoute
		if wd != workingDir {
			t.Fatalf("working dir = %q, want %q", wd, workingDir)
		}
		return backend
	})

	factory, ok := Get("bash")
	if !ok {
		t.Fatal("bash tool not registered")
	}
	tool := factory(context.Background())
	rawArgs, _ := json.Marshal(TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test default sandbox route",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "echo hi",
	})

	obs := tool.Execute(ContextWithSessionID(context.Background(), "session-default-route"), rawArgs)
	output, ok := obs.(*TerminalObservation)
	if !ok {
		t.Fatalf("expected *TerminalObservation, got %T", obs)
	}
	if gotRoute != bashRuntimeLabelSandbox {
		t.Fatalf("route = %q, want %q", gotRoute, bashRuntimeLabelSandbox)
	}
	if output.ExitCodeValue() != 0 {
		t.Fatalf("expected exit code 0, got %d, content: %q", output.ExitCodeValue(), output.OutputText())
	}
	if output.OutputText() != "ok\n" {
		t.Fatalf("expected output ok\\n, got %q", output.OutputText())
	}
	if len(backend.sentPaneIDs) != 1 || backend.sentPaneIDs[0] != "default_main" {
		t.Fatalf("pane ids = %#v, want [default_main]", backend.sentPaneIDs)
	}
}

func TestBashDefaultToolReturnsStableErrorWhenSandboxInitFails(t *testing.T) {
	workingDir := t.TempDir()
	var gotRoute string

	installBashToolTestHooks(t, workingDir, func(runtimeRoute string, wd string) terminalpkg.TerminalBackend {
		gotRoute = runtimeRoute
		return &fakeToolBackend{
			workingDir:    wd,
			initializeErr: context.DeadlineExceeded,
		}
	})

	factory, ok := Get("bash")
	if !ok {
		t.Fatal("bash tool not registered")
	}
	tool := factory(context.Background())
	rawArgs, _ := json.Marshal(TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test sandbox init failure",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "echo hi",
	})

	obs := tool.Execute(ContextWithSessionID(context.Background(), "session-default-sandbox"), rawArgs)
	output, ok := obs.(*TerminalObservation)
	if !ok {
		t.Fatalf("expected *TerminalObservation, got %T", obs)
	}
	if gotRoute != bashRuntimeLabelSandbox {
		t.Fatalf("route = %q, want %q", gotRoute, bashRuntimeLabelSandbox)
	}
	if output.ExitCodeValue() != -1 {
		t.Fatalf("expected exit code -1, got %d", output.ExitCodeValue())
	}
	if !strings.Contains(output.OutputText(), "初始化终端会话") {
		t.Fatalf("expected initialize error, got %q", output.OutputText())
	}
}

func TestHostBashToolUsesExplicitHostBackend(t *testing.T) {
	workingDir := t.TempDir()
	backend := &fakeToolBackend{workingDir: workingDir}
	var gotRoute string

	installBashToolTestHooks(t, workingDir, func(runtimeRoute string, wd string) terminalpkg.TerminalBackend {
		gotRoute = runtimeRoute
		return backend
	})

	tool := newHostBashTool()
	rawArgs, _ := json.Marshal(TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test explicit host route",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "echo hi",
	})

	obs := tool.Execute(context.Background(), rawArgs)
	output, ok := obs.(*TerminalObservation)
	if !ok {
		t.Fatalf("expected *TerminalObservation, got %T", obs)
	}
	if gotRoute != bashRuntimeLabelHost {
		t.Fatalf("route = %q, want %q", gotRoute, bashRuntimeLabelHost)
	}
	if output.ExitCodeValue() != 0 {
		t.Fatalf("expected exit code 0, got %d", output.ExitCodeValue())
	}
}

func TestBashNameAndDescription(t *testing.T) {
	tool := newHostBashTool()
	if tool.Name() != "bash" {
		t.Fatalf("expected name 'bash', got %q", tool.Name())
	}
	if tool.Description() == "" {
		t.Fatal("expected non-empty description")
	}
}

func TestBashActionSchema(t *testing.T) {
	converted, err := ToOpenAITool(newHostBashTool())
	if err != nil {
		t.Fatalf("ToOpenAITool failed: %v", err)
	}

	functionValue, ok := converted["function"].(map[string]any)
	if !ok {
		t.Fatalf("expected function map, got %T", converted["function"])
	}
	parameters, ok := functionValue["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("expected parameters map, got %T", functionValue["parameters"])
	}
	properties, ok := parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties map, got %T", parameters["properties"])
	}

	for _, name := range []string{"command", "is_input", "timeout", "reset"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("expected action field %q in schema", name)
		}
	}
	if _, ok := properties["working_dir"]; ok {
		t.Fatal("working_dir should not appear in action schema")
	}
	if _, ok := properties["timeout_secs"]; ok {
		t.Fatal("timeout_secs should not appear in action schema")
	}
}

func TestBashInvalidJSONReturnsErrorObservation(t *testing.T) {
	obs := newHostBashTool().Execute(context.Background(), []byte("{"))
	if obs == nil {
		t.Fatal("expected observation")
	}
	if !obs.IsError() {
		t.Fatal("expected error observation")
	}
}

func TestBashDefaultToolReusesExecutorWithinSameSession(t *testing.T) {
	workingDir := t.TempDir()
	var createCount int

	installBashToolTestHooks(t, workingDir, func(runtimeRoute string, wd string) terminalpkg.TerminalBackend {
		createCount++
		if runtimeRoute != bashRuntimeLabelSandbox {
			t.Fatalf("route = %q, want %q", runtimeRoute, bashRuntimeLabelSandbox)
		}
		return &fakeToolBackend{workingDir: wd}
	})

	factory, ok := Get("bash")
	if !ok {
		t.Fatal("bash tool not registered")
	}
	rawArgs, _ := json.Marshal(TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test session executor reuse",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "echo hi",
		PaneID:  "pane-a",
	})

	sessionCtx := ContextWithSessionID(context.Background(), "session-reuse")
	toolA := factory(context.Background())
	toolB := factory(context.Background())
	if obs := toolA.Execute(sessionCtx, rawArgs).(*TerminalObservation); obs.ExitCodeValue() != 0 {
		t.Fatalf("first call exit code = %d, output = %q", obs.ExitCodeValue(), obs.OutputText())
	}
	if obs := toolB.Execute(sessionCtx, rawArgs).(*TerminalObservation); obs.ExitCodeValue() != 0 {
		t.Fatalf("second call exit code = %d, output = %q", obs.ExitCodeValue(), obs.OutputText())
	}
	if createCount != 1 {
		t.Fatalf("backend create count = %d, want 1", createCount)
	}
}

func TestBashDefaultToolSeparatesExecutorsAcrossSessions(t *testing.T) {
	workingDir := t.TempDir()
	var createCount int

	installBashToolTestHooks(t, workingDir, func(runtimeRoute string, wd string) terminalpkg.TerminalBackend {
		createCount++
		return &fakeToolBackend{workingDir: wd}
	})

	factory, ok := Get("bash")
	if !ok {
		t.Fatal("bash tool not registered")
	}
	rawArgs, _ := json.Marshal(TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test session isolation",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "echo hi",
		PaneID:  "pane-a",
	})

	toolA := factory(context.Background())
	toolB := factory(context.Background())
	if obs := toolA.Execute(ContextWithSessionID(context.Background(), "session-a"), rawArgs).(*TerminalObservation); obs.ExitCodeValue() != 0 {
		t.Fatalf("session-a exit code = %d, output = %q", obs.ExitCodeValue(), obs.OutputText())
	}
	if obs := toolB.Execute(ContextWithSessionID(context.Background(), "session-b"), rawArgs).(*TerminalObservation); obs.ExitCodeValue() != 0 {
		t.Fatalf("session-b exit code = %d, output = %q", obs.ExitCodeValue(), obs.OutputText())
	}
	if createCount != 2 {
		t.Fatalf("backend create count = %d, want 2", createCount)
	}
}

func TestBashDefaultToolRemovesFailedSessionExecutorAfterInitializeError(t *testing.T) {
	workingDir := t.TempDir()
	firstBackend := &fakeToolBackend{
		workingDir:    workingDir,
		initializeErr: context.DeadlineExceeded,
	}
	secondBackend := &fakeToolBackend{workingDir: workingDir}
	createCount := 0

	installBashToolTestHooks(t, workingDir, func(runtimeRoute string, wd string) terminalpkg.TerminalBackend {
		createCount++
		if createCount == 1 {
			return firstBackend
		}
		return secondBackend
	})

	factory, ok := Get("bash")
	if !ok {
		t.Fatal("bash tool not registered")
	}
	rawArgs, _ := json.Marshal(TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test failed executor cleanup",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "echo hi",
		PaneID:  "pane-a",
	})

	sessionCtx := ContextWithSessionID(context.Background(), "session-retry")
	toolA := factory(context.Background())
	firstObs := toolA.Execute(sessionCtx, rawArgs).(*TerminalObservation)
	if firstObs.ExitCodeValue() != -1 {
		t.Fatalf("first exit code = %d, want -1", firstObs.ExitCodeValue())
	}
	if firstBackend.closeCount != 1 {
		t.Fatalf("first backend close count = %d, want 1", firstBackend.closeCount)
	}

	toolB := factory(context.Background())
	secondObs := toolB.Execute(sessionCtx, rawArgs).(*TerminalObservation)
	if secondObs.ExitCodeValue() != 0 {
		t.Fatalf("second exit code = %d, output = %q", secondObs.ExitCodeValue(), secondObs.OutputText())
	}
	if createCount != 2 {
		t.Fatalf("backend create count = %d, want 2", createCount)
	}
}

func TestReleaseBashSessionClosesCachedExecutor(t *testing.T) {
	workingDir := t.TempDir()
	backend := &fakeToolBackend{workingDir: workingDir}
	var createCount int

	installBashToolTestHooks(t, workingDir, func(runtimeRoute string, wd string) terminalpkg.TerminalBackend {
		createCount++
		return backend
	})

	factory, ok := Get("bash")
	if !ok {
		t.Fatal("bash tool not registered")
	}
	rawArgs, _ := json.Marshal(TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test release bash session",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "echo hi",
		PaneID:  "pane-a",
	})

	sessionID := "session-release"
	sessionCtx := ContextWithSessionID(context.Background(), sessionID)
	toolA := factory(context.Background())
	if obs := toolA.Execute(sessionCtx, rawArgs).(*TerminalObservation); obs.ExitCodeValue() != 0 {
		t.Fatalf("first exit code = %d, output = %q", obs.ExitCodeValue(), obs.OutputText())
	}
	if err := ReleaseBashSession(context.Background(), sessionID); err != nil {
		t.Fatalf("ReleaseBashSession() error = %v", err)
	}
	if backend.closeCount != 1 {
		t.Fatalf("backend close count = %d, want 1", backend.closeCount)
	}

	recreatedBackend := &fakeToolBackend{workingDir: workingDir}
	newSandboxBashBackendBundle = func(ctx context.Context, wd string) bashBackendBundle {
		_ = ctx
		createCount++
		return bashBackendBundle{backend: recreatedBackend}
	}
	toolB := factory(context.Background())
	if obs := toolB.Execute(sessionCtx, rawArgs).(*TerminalObservation); obs.ExitCodeValue() != 0 {
		t.Fatalf("second exit code = %d, output = %q", obs.ExitCodeValue(), obs.OutputText())
	}
	if createCount != 2 {
		t.Fatalf("backend create count = %d, want 2", createCount)
	}
}

func TestBashDefaultToolRemovesFailedSessionExecutorAfterRuntimeFailure(t *testing.T) {
	workingDir := t.TempDir()
	firstBackend := &fakeToolBackend{
		workingDir: workingDir,
		readErr:    errors.New("tmux session disappeared"),
	}
	secondBackend := &fakeToolBackend{workingDir: workingDir}
	createCount := 0

	installBashToolTestHooks(t, workingDir, func(runtimeRoute string, wd string) terminalpkg.TerminalBackend {
		createCount++
		if createCount == 1 {
			return firstBackend
		}
		return secondBackend
	})

	factory, ok := Get("bash")
	if !ok {
		t.Fatal("bash tool not registered")
	}
	rawArgs, _ := json.Marshal(TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test failed executor runtime cleanup",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "echo hi",
		PaneID:  "pane-a",
	})

	sessionCtx := ContextWithSessionID(context.Background(), "session-runtime-failure")
	firstObs := factory(context.Background()).Execute(sessionCtx, rawArgs).(*TerminalObservation)
	if firstObs.ExitCodeValue() != -1 {
		t.Fatalf("first exit code = %d, want -1", firstObs.ExitCodeValue())
	}
	if !strings.Contains(firstObs.OutputText(), "读取终端屏幕") && !strings.Contains(firstObs.OutputText(), "执行命令前读取终端屏幕") {
		t.Fatalf("expected read screen error, got %q", firstObs.OutputText())
	}
	if firstBackend.closeCount != 1 {
		t.Fatalf("first backend close count = %d, want 1", firstBackend.closeCount)
	}

	secondObs := factory(context.Background()).Execute(sessionCtx, rawArgs).(*TerminalObservation)
	if secondObs.ExitCodeValue() != 0 {
		t.Fatalf("second exit code = %d, output = %q", secondObs.ExitCodeValue(), secondObs.OutputText())
	}
	if createCount != 2 {
		t.Fatalf("backend create count = %d, want 2", createCount)
	}
}

func TestBashDefaultToolRequiresSessionID(t *testing.T) {
	workingDir := t.TempDir()
	installBashToolTestHooks(t, workingDir, func(runtimeRoute string, wd string) terminalpkg.TerminalBackend {
		return &fakeToolBackend{workingDir: wd}
	})

	factory, ok := Get("bash")
	if !ok {
		t.Fatal("bash tool not registered")
	}
	rawArgs, _ := json.Marshal(TerminalAction{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test missing session id",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "echo hi",
		PaneID:  "pane-a",
	})

	obs := factory(context.Background()).Execute(context.Background(), rawArgs).(*TerminalObservation)
	if obs.ExitCodeValue() != -1 {
		t.Fatalf("exit code = %d, want -1", obs.ExitCodeValue())
	}
	if !strings.Contains(obs.OutputText(), "session.id") {
		t.Fatalf("expected missing session.id error, got %q", obs.OutputText())
	}
}

func TestBashSessionRegistryRejectsDifferentWorkingDirForSameSession(t *testing.T) {
	workingDirA := t.TempDir()
	workingDirB := t.TempDir()
	var createCount int

	installBashToolTestHooks(t, workingDirA, func(runtimeRoute string, wd string) terminalpkg.TerminalBackend {
		createCount++
		if runtimeRoute != bashRuntimeLabelSandbox {
			t.Fatalf("route = %q, want %q", runtimeRoute, bashRuntimeLabelSandbox)
		}
		return &fakeToolBackend{workingDir: wd}
	})

	registry := newBashSessionRegistry()
	sessionID := "session-workingdir-mismatch"

	firstExecutor, err := registry.executorForSession(context.Background(), sessionID, bashRuntimeLabelSandbox, workingDirA, func(ctx context.Context, wd string) bashBackendBundle {
		return newSandboxBashBackendBundle(ctx, wd)
	})
	if err != nil {
		t.Fatalf("first executorForSession() error = %v", err)
	}
	if firstExecutor == nil {
		t.Fatal("expected first executor")
	}

	secondExecutor, err := registry.executorForSession(context.Background(), sessionID, bashRuntimeLabelSandbox, workingDirB, func(ctx context.Context, wd string) bashBackendBundle {
		return newSandboxBashBackendBundle(ctx, wd)
	})
	if err == nil {
		t.Fatal("expected working_dir mismatch error")
	}
	if secondExecutor != nil {
		t.Fatalf("expected nil second executor, got %#v", secondExecutor)
	}
	if !strings.Contains(err.Error(), "工作目录") {
		t.Fatalf("expected working dir error, got %v", err)
	}
	if createCount != 1 {
		t.Fatalf("backend create count = %d, want 1", createCount)
	}
}

func TestResolveBashExecutorHostRouteDoesNotUseSessionCache(t *testing.T) {
	workingDir := t.TempDir()
	var createCount int

	installBashToolTestHooks(t, workingDir, func(runtimeRoute string, wd string) terminalpkg.TerminalBackend {
		createCount++
		if runtimeRoute != bashRuntimeLabelHost {
			t.Fatalf("route = %q, want %q", runtimeRoute, bashRuntimeLabelHost)
		}
		return &fakeToolBackend{workingDir: wd}
	})

	sessionID := "session-host-route"
	hostCtx := ContextWithSessionID(context.Background(), sessionID)

	firstExecutor, err := resolveEphemeralBashExecutor(hostCtx, workingDir, newHostBashBackendBundle)
	if err != nil {
		t.Fatalf("first resolveBashExecutor() error = %v", err)
	}
	secondExecutor, err := resolveEphemeralBashExecutor(hostCtx, workingDir, newHostBashBackendBundle)
	if err != nil {
		t.Fatalf("second resolveBashExecutor() error = %v", err)
	}
	if firstExecutor == nil || secondExecutor == nil {
		t.Fatal("expected non-nil executors")
	}
	if firstExecutor == secondExecutor {
		t.Fatal("expected host route executors to be distinct")
	}
	if createCount != 2 {
		t.Fatalf("backend create count = %d, want 2", createCount)
	}
}

func TestBashSessionRegistryInvalidateSessionDoesNotDeleteReplacementEntry(t *testing.T) {
	workingDir := t.TempDir()
	firstBackend := &fakeToolBackend{workingDir: workingDir}
	replacementBackend := &fakeToolBackend{workingDir: workingDir}
	createCount := 0

	installBashToolTestHooks(t, workingDir, func(runtimeRoute string, wd string) terminalpkg.TerminalBackend {
		createCount++
		if createCount == 1 {
			return firstBackend
		}
		return replacementBackend
	})

	registry := newBashSessionRegistry()
	sessionID := "session-replacement-safety"

	firstExecutor, err := registry.executorForSession(context.Background(), sessionID, bashRuntimeLabelSandbox, workingDir, func(ctx context.Context, wd string) bashBackendBundle {
		return newSandboxBashBackendBundle(ctx, wd)
	})
	if err != nil {
		t.Fatalf("first executorForSession() error = %v", err)
	}
	firstEntry := registry.sessions[sessionID]
	if firstEntry == nil {
		t.Fatal("expected first cached entry")
	}

	if releaseErr := registry.releaseSession(context.Background(), sessionID); releaseErr != nil {
		t.Fatalf("unexpected release error: %v", releaseErr)
	}

	replacementExecutor, err := registry.executorForSession(context.Background(), sessionID, bashRuntimeLabelSandbox, workingDir, func(ctx context.Context, wd string) bashBackendBundle {
		return newSandboxBashBackendBundle(ctx, wd)
	})
	if err != nil {
		t.Fatalf("replacement executorForSession() error = %v", err)
	}
	replacementEntry := registry.sessions[sessionID]
	if replacementEntry == nil {
		t.Fatal("expected replacement cached entry")
	}

	registry.invalidateSession(context.Background(), sessionID, firstEntry, errors.New("late failure callback"))

	currentEntry := registry.sessions[sessionID]
	if currentEntry != replacementEntry {
		t.Fatal("expected replacement entry to remain cached")
	}
	if replacementExecutor == nil || firstExecutor == nil {
		t.Fatal("expected non-nil executors")
	}
}

func TestBashSessionRegistryAuditArgsUseSandboxInfoProviderInsteadOfBackendAssertion(t *testing.T) {
	workingDir := t.TempDir()
	info := sandboxpkg.Info{
		Status:        sandboxpkg.StatusRunning,
		Image:         "sandbox-image",
		ContainerName: "sandbox-container",
	}

	installBashToolBundleTestHooks(t, workingDir, func(runtimeRoute string, wd string) bashBackendBundle {
		if runtimeRoute != bashRuntimeLabelSandbox {
			t.Fatalf("route = %q, want %q", runtimeRoute, bashRuntimeLabelSandbox)
		}
		return bashBackendBundle{
			backend: &fakeToolBackend{workingDir: wd},
			sandboxInfoProvider: func() sandboxpkg.Info {
				return info
			},
		}
	})

	registry := newBashSessionRegistry()
	sessionID := "session-audit-provider"
	executor, err := registry.executorForSession(context.Background(), sessionID, bashRuntimeLabelSandbox, workingDir, func(ctx context.Context, wd string) bashBackendBundle {
		return newSandboxBashBackendBundle(ctx, wd)
	})
	if err != nil {
		t.Fatalf("executorForSession() error = %v", err)
	}
	if executor == nil {
		t.Fatal("expected executor")
	}

	args := registry.auditArgsForSession(sessionID)
	if !containsAuditArg(args, "sandbox_status", string(info.Status)) {
		t.Fatalf("expected sandbox_status in args, got %#v", args)
	}
	if !containsAuditArg(args, "sandbox_image", info.Image) {
		t.Fatalf("expected sandbox_image in args, got %#v", args)
	}
	if !containsAuditArg(args, "sandbox_container", info.ContainerName) {
		t.Fatalf("expected sandbox_container in args, got %#v", args)
	}
}

func containsAuditArg(args []any, key string, value any) bool {
	for i := 0; i+1 < len(args); i += 2 {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}
