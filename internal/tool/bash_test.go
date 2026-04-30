package tool

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	terminalpkg "github.com/wen/opentalon/internal/tool/terminal"
	"github.com/wen/opentalon/internal/types"
)

type fakeToolBackend struct {
	mu            sync.Mutex
	screen        string
	workingDir    string
	initializeErr error
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
	return nil
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

func installBashToolTestHooks(t *testing.T, workingDir string, factory func(route bashRuntimeRoute, workingDir string) terminalpkg.TerminalBackend) {
	t.Helper()

	prevResolve := resolveBashWorkingDir
	prevBackendFactory := newBashBackendForRoute
	prevRegistry := bashSessionExecutors
	resolveBashWorkingDir = func() string {
		return workingDir
	}
	newBashBackendForRoute = func(ctx context.Context, route bashRuntimeRoute, wd string) terminalpkg.TerminalBackend {
		_ = ctx
		return factory(route, wd)
	}
	t.Cleanup(func() {
		resolveBashWorkingDir = prevResolve
		newBashBackendForRoute = prevBackendFactory
		bashSessionExecutors = prevRegistry
	})
	bashSessionExecutors = newBashSessionRegistry()
}

func TestBashRegisteredToolUsesSandboxBackendByDefault(t *testing.T) {
	workingDir := t.TempDir()
	backend := &fakeToolBackend{workingDir: workingDir}
	var gotRoute bashRuntimeRoute

	installBashToolTestHooks(t, workingDir, func(route bashRuntimeRoute, wd string) terminalpkg.TerminalBackend {
		gotRoute = route
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
	if gotRoute != bashRuntimeRouteSandbox {
		t.Fatalf("route = %q, want %q", gotRoute, bashRuntimeRouteSandbox)
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
	var gotRoute bashRuntimeRoute

	installBashToolTestHooks(t, workingDir, func(route bashRuntimeRoute, wd string) terminalpkg.TerminalBackend {
		gotRoute = route
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
	if gotRoute != bashRuntimeRouteSandbox {
		t.Fatalf("route = %q, want %q", gotRoute, bashRuntimeRouteSandbox)
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
	var gotRoute bashRuntimeRoute

	installBashToolTestHooks(t, workingDir, func(route bashRuntimeRoute, wd string) terminalpkg.TerminalBackend {
		gotRoute = route
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
	if gotRoute != bashRuntimeRouteHost {
		t.Fatalf("route = %q, want %q", gotRoute, bashRuntimeRouteHost)
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

	installBashToolTestHooks(t, workingDir, func(route bashRuntimeRoute, wd string) terminalpkg.TerminalBackend {
		createCount++
		if route != bashRuntimeRouteSandbox {
			t.Fatalf("route = %q, want %q", route, bashRuntimeRouteSandbox)
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

	installBashToolTestHooks(t, workingDir, func(route bashRuntimeRoute, wd string) terminalpkg.TerminalBackend {
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

	installBashToolTestHooks(t, workingDir, func(route bashRuntimeRoute, wd string) terminalpkg.TerminalBackend {
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

	installBashToolTestHooks(t, workingDir, func(route bashRuntimeRoute, wd string) terminalpkg.TerminalBackend {
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
	newBashBackendForRoute = func(ctx context.Context, route bashRuntimeRoute, wd string) terminalpkg.TerminalBackend {
		_ = ctx
		createCount++
		return recreatedBackend
	}
	toolB := factory(context.Background())
	if obs := toolB.Execute(sessionCtx, rawArgs).(*TerminalObservation); obs.ExitCodeValue() != 0 {
		t.Fatalf("second exit code = %d, output = %q", obs.ExitCodeValue(), obs.OutputText())
	}
	if createCount != 2 {
		t.Fatalf("backend create count = %d, want 2", createCount)
	}
}

func TestBashDefaultToolRequiresSessionID(t *testing.T) {
	workingDir := t.TempDir()
	installBashToolTestHooks(t, workingDir, func(route bashRuntimeRoute, wd string) terminalpkg.TerminalBackend {
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

	installBashToolTestHooks(t, workingDirA, func(route bashRuntimeRoute, wd string) terminalpkg.TerminalBackend {
		createCount++
		if route != bashRuntimeRouteSandbox {
			t.Fatalf("route = %q, want %q", route, bashRuntimeRouteSandbox)
		}
		return &fakeToolBackend{workingDir: wd}
	})

	registry := newBashSessionRegistry()
	sessionID := "session-workingdir-mismatch"

	firstExecutor, err := registry.executorForSession(context.Background(), sessionID, bashRuntimeRouteSandbox, workingDirA)
	if err != nil {
		t.Fatalf("first executorForSession() error = %v", err)
	}
	if firstExecutor == nil {
		t.Fatal("expected first executor")
	}

	secondExecutor, err := registry.executorForSession(context.Background(), sessionID, bashRuntimeRouteSandbox, workingDirB)
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

func TestBashSessionRegistryHostRouteDoesNotUseSessionCache(t *testing.T) {
	workingDir := t.TempDir()
	var createCount int

	installBashToolTestHooks(t, workingDir, func(route bashRuntimeRoute, wd string) terminalpkg.TerminalBackend {
		createCount++
		if route != bashRuntimeRouteHost {
			t.Fatalf("route = %q, want %q", route, bashRuntimeRouteHost)
		}
		return &fakeToolBackend{workingDir: wd}
	})

	registry := newBashSessionRegistry()
	sessionID := "session-host-route"

	firstExecutor, err := registry.executorForSession(context.Background(), sessionID, bashRuntimeRouteHost, workingDir)
	if err != nil {
		t.Fatalf("first executorForSession() error = %v", err)
	}
	secondExecutor, err := registry.executorForSession(context.Background(), sessionID, bashRuntimeRouteHost, workingDir)
	if err != nil {
		t.Fatalf("second executorForSession() error = %v", err)
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
