package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wen/opentalon/pkg/observability"
)

func TestManagerCreateReturnsDockerSandboxByDefault(t *testing.T) {
	manager := NewManager(nil)

	sb := manager.Create(Config{WorkingDir: "/tmp/project"})
	if sb == nil {
		t.Fatal("expected sandbox instance")
	}
	if _, ok := sb.(*DockerSandbox); !ok {
		t.Fatalf("expected DockerSandbox, got %T", sb)
	}

	info := sb.Info()
	if info.Status != StatusCreated {
		t.Fatalf("status = %q, want %q", info.Status, StatusCreated)
	}
	if info.HostWorkingDir != "/tmp/project" {
		t.Fatalf("working dir = %q, want %q", info.HostWorkingDir, "/tmp/project")
	}
	if info.Image != DefaultDockerImage {
		t.Fatalf("image = %q, want %q", info.Image, DefaultDockerImage)
	}
	if info.ContainerWorkDir != DefaultContainerWorkDir {
		t.Fatalf("container work dir = %q, want %q", info.ContainerWorkDir, DefaultContainerWorkDir)
	}
}

func TestUnimplementedSandboxCloseIsStable(t *testing.T) {
	sb := NewUnimplementedSandbox(Config{WorkingDir: "/tmp/project"})

	if err := sb.Close(context.Background()); err != nil {
		t.Fatalf("first close returned error: %v", err)
	}
	if err := sb.Close(context.Background()); err != nil {
		t.Fatalf("second close returned error: %v", err)
	}

	if got := sb.Info().Status; got != StatusClosed {
		t.Fatalf("status after close = %q, want %q", got, StatusClosed)
	}
}

func TestUnimplementedSandboxStartReturnsStableError(t *testing.T) {
	sb := NewUnimplementedSandbox(Config{})

	err := sb.Start(context.Background())
	if !errors.Is(err, ErrRuntimeNotImplemented) {
		t.Fatalf("expected ErrRuntimeNotImplemented, got %v", err)
	}

	if got := sb.Info().Status; got != StatusCreated {
		t.Fatalf("status after failed start = %q, want %q", got, StatusCreated)
	}
}

func TestDockerSandboxUsesDefaultImageAndWorkDir(t *testing.T) {
	sb := NewDockerSandbox(Config{WorkingDir: "/tmp/project"})

	info := sb.Info()
	if info.Image != DefaultDockerImage {
		t.Fatalf("image = %q, want %q", info.Image, DefaultDockerImage)
	}
	if info.ContainerWorkDir != DefaultContainerWorkDir {
		t.Fatalf("container work dir = %q, want %q", info.ContainerWorkDir, DefaultContainerWorkDir)
	}
	if info.HostWorkingDir != "/tmp/project" {
		t.Fatalf("host working dir = %q, want %q", info.HostWorkingDir, "/tmp/project")
	}
}

func TestDockerSandboxStartExecAndClose(t *testing.T) {
	runner := &fakeDockerRunner{
		lookPath: map[string]string{
			"docker": "/usr/bin/docker",
		},
	}
	sb := NewDockerSandbox(Config{
		WorkingDir:       "/tmp/project",
		ContainerName:    "sandbox-test",
		ContainerWorkDir: "/workspace",
	})
	sb.runner = runner

	if err := sb.Start(context.Background()); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if got := sb.Info().Status; got != StatusRunning {
		t.Fatalf("status after start = %q, want %q", got, StatusRunning)
	}

	out, err := sb.Exec(context.Background(), "pwd")
	if err != nil {
		t.Fatalf("exec failed: %v", err)
	}
	if out != "ok\n" {
		t.Fatalf("exec output = %q, want %q", out, "ok\n")
	}

	if err := sb.Close(context.Background()); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	if err := sb.Close(context.Background()); err != nil {
		t.Fatalf("second close failed: %v", err)
	}
	if got := sb.Info().Status; got != StatusClosed {
		t.Fatalf("status after close = %q, want %q", got, StatusClosed)
	}

	if len(runner.calls) != 3 {
		t.Fatalf("call count = %d, want %d", len(runner.calls), 3)
	}
	runArgs := strings.Join(runner.calls[0], " ")
	if !strings.Contains(runArgs, "run -d --rm --name sandbox-test --read-only --memory 1073741824 --pids-limit 128 -w /workspace") {
		t.Fatalf("unexpected docker run args: %v", runner.calls[0])
	}
	if strings.Contains(runArgs, "--cpus") || strings.Contains(runArgs, "--cpu-quota") || strings.Contains(runArgs, "--cpuset-cpus") {
		t.Fatalf("did not expect cpu limit args, got %v", runner.calls[0])
	}
	if !strings.Contains(runArgs, "-v /tmp/project:/workspace") {
		t.Fatalf("expected writable workspace mount, got %v", runner.calls[0])
	}
	if got := runner.calls[1]; len(got) < 6 || got[0] != "docker" || got[1] != "exec" || got[2] != "-w" || got[3] != "/workspace" || got[4] != "sandbox-test" || got[5] != "pwd" {
		t.Fatalf("unexpected docker exec args: %v", got)
	}
	if got := runner.calls[2]; len(got) != 4 || got[0] != "docker" || got[1] != "rm" || got[2] != "-f" || got[3] != "sandbox-test" {
		t.Fatalf("unexpected docker rm args: %v", got)
	}
}

func TestDockerSandboxExecRequiresRunningSandbox(t *testing.T) {
	sb := NewDockerSandbox(Config{})

	if _, err := sb.Exec(context.Background(), "pwd"); !errors.Is(err, ErrSandboxNotRunning) {
		t.Fatalf("expected ErrSandboxNotRunning, got %v", err)
	}

	_ = sb.Close(context.Background())
	if _, err := sb.Exec(context.Background(), "pwd"); !errors.Is(err, ErrSandboxClosed) {
		t.Fatalf("expected ErrSandboxClosed, got %v", err)
	}
}

func TestDockerSandboxStartReturnsStableErrorWhenDockerFails(t *testing.T) {
	runner := &fakeDockerRunner{
		lookPath: map[string]string{
			"docker": "/usr/bin/docker",
		},
		runErr: fmt.Errorf("boom"),
	}
	sb := NewDockerSandbox(Config{ContainerName: "sandbox-test"})
	sb.runner = runner

	err := sb.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "failed to start docker sandbox") {
		t.Fatalf("unexpected start error: %v", err)
	}
}

func TestDockerSandboxStartRejectsHighRiskReadOnlyMount(t *testing.T) {
	workspace := t.TempDir()
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	sb := NewDockerSandbox(Config{
		WorkingDir: workspace,
		ReadOnlyMounts: []Mount{
			{HostPath: filepath.Join(homeDir, ".ssh"), ContainerPath: "/host-ssh"},
		},
	})
	sb.runner = &fakeDockerRunner{
		lookPath: map[string]string{"docker": "/usr/bin/docker"},
	}

	err := sb.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected readonly mount policy error, got %v", err)
	}
}

func TestDockerSandboxExecTimesOutWithStableError(t *testing.T) {
	workspace := t.TempDir()
	restore := installFakeTracer()
	defer restore()

	runner := &fakeDockerRunner{
		lookPath: map[string]string{"docker": "/usr/bin/docker"},
		runFunc: func(ctx context.Context, name string, args ...string) (dockerCommandResult, error) {
			if len(args) > 0 && args[0] == "run" {
				return dockerCommandResult{}, nil
			}
			<-ctx.Done()
			return dockerCommandResult{}, ctx.Err()
		},
	}
	sb := NewDockerSandbox(Config{
		WorkingDir:    workspace,
		ContainerName: "sandbox-test",
		ExecTimeout:   10 * time.Millisecond,
	})
	sb.runner = runner

	if err := sb.Start(context.Background()); err != nil {
		t.Fatalf("start failed: %v", err)
	}

	_, err := sb.Exec(context.Background(), "sleep", "10")
	if !errors.Is(err, ErrSandboxTimedOut) {
		t.Fatalf("expected ErrSandboxTimedOut, got %v", err)
	}
}

func TestDockerSandboxExecTruncatesOutputAndAudits(t *testing.T) {
	workspace := t.TempDir()
	tracer := &fakeTracer{}
	restore := replaceTracer(tracer)
	defer restore()

	runner := &fakeDockerRunner{
		lookPath: map[string]string{"docker": "/usr/bin/docker"},
		runFunc: func(ctx context.Context, name string, args ...string) (dockerCommandResult, error) {
			_ = ctx
			if len(args) > 0 && args[0] == "run" {
				return dockerCommandResult{}, nil
			}
			return dockerCommandResult{
				Stdout:   "1234567890",
				Stderr:   "ERR",
				ExitCode: 0,
			}, nil
		},
	}
	sb := NewDockerSandbox(Config{
		WorkingDir:       workspace,
		ContainerName:    "sandbox-test",
		OutputLimitBytes: 8,
	})
	sb.runner = runner

	if err := sb.Start(context.Background()); err != nil {
		t.Fatalf("start failed: %v", err)
	}

	out, err := sb.Exec(context.Background(), "echo", "hello")
	if err != nil {
		t.Fatalf("exec failed: %v", err)
	}
	if !strings.Contains(out, "truncated") {
		t.Fatalf("expected truncated marker, got %q", out)
	}
	if len(tracer.spans) != 1 {
		t.Fatalf("expected one span, got %d", len(tracer.spans))
	}
	span := tracer.spans[0]
	if !span.boolAttr("sandbox.output_truncated") {
		t.Fatalf("expected output truncated attr, attrs=%#v", span.attrs)
	}
	if span.stringAttr("sandbox.command") != "echo hello" {
		t.Fatalf("expected command attr, attrs=%#v", span.attrs)
	}
	if span.stringAttr("sandbox.runtime") != "docker" {
		t.Fatalf("expected runtime attr, attrs=%#v", span.attrs)
	}
	if got := span.int64Attr("sandbox.memory_limit_bytes"); got != DefaultMemoryLimitBytes {
		t.Fatalf("memory limit attr = %d, want %d", got, DefaultMemoryLimitBytes)
	}
	if got := span.intAttr("sandbox.pid_limit"); got != DefaultProcessLimit {
		t.Fatalf("pid limit attr = %d, want %d", got, DefaultProcessLimit)
	}
	if len(span.events) == 0 || span.events[0].name != "sandbox.output.truncated" {
		t.Fatalf("expected truncation event, events=%#v", span.events)
	}
}

func TestDockerSandboxExecResetsStateWhenContainerIsUnavailable(t *testing.T) {
	workspace := t.TempDir()
	tracer := &fakeTracer{}
	restore := replaceTracer(tracer)
	defer restore()

	runner := &fakeDockerRunner{
		lookPath: map[string]string{"docker": "/usr/bin/docker"},
		runFunc: func(ctx context.Context, name string, args ...string) (dockerCommandResult, error) {
			_ = ctx
			if len(args) > 0 && args[0] == "run" {
				return dockerCommandResult{}, nil
			}
			return dockerCommandResult{
				Stderr:   "Error response from daemon: Container sandbox-test is not running",
				ExitCode: 1,
			}, errors.New("container is not running")
		},
	}
	sb := NewDockerSandbox(Config{
		WorkingDir:    workspace,
		ContainerName: "sandbox-test",
	})
	sb.runner = runner

	if err := sb.Start(context.Background()); err != nil {
		t.Fatalf("start failed: %v", err)
	}

	_, err := sb.Exec(context.Background(), "pwd")
	if !errors.Is(err, ErrSandboxNotRunning) {
		t.Fatalf("expected ErrSandboxNotRunning, got %v", err)
	}
	info := sb.Info()
	if info.Status != StatusCreated {
		t.Fatalf("status after recovery = %q, want %q", info.Status, StatusCreated)
	}
	if info.ContainerName != "" {
		t.Fatalf("container name after recovery = %q, want empty", info.ContainerName)
	}
	if len(tracer.spans) != 1 {
		t.Fatalf("expected one span, got %d", len(tracer.spans))
	}
	span := tracer.spans[0]
	if !span.boolAttr("sandbox.recovery_attempted") {
		t.Fatalf("expected recovery_attempted attr, attrs=%#v", span.attrs)
	}
	if got := span.stringAttr("sandbox.recovery_result"); got != "reset_to_created" {
		t.Fatalf("recovery_result = %q, want %q", got, "reset_to_created")
	}
	if len(span.events) == 0 || span.events[0].name != "sandbox.recovery" {
		t.Fatalf("expected recovery event, got %#v", span.events)
	}
}

func TestDockerSandboxCleanupIfIdleClosesSandboxAndAudits(t *testing.T) {
	workspace := t.TempDir()
	tracer := &fakeTracer{}
	restore := replaceTracer(tracer)
	defer restore()

	runner := &fakeDockerRunner{
		lookPath: map[string]string{"docker": "/usr/bin/docker"},
		runFunc: func(ctx context.Context, name string, args ...string) (dockerCommandResult, error) {
			_ = ctx
			_ = name
			return dockerCommandResult{}, nil
		},
	}
	sb := NewDockerSandbox(Config{
		WorkingDir:    workspace,
		ContainerName: "sandbox-test",
	})
	sb.runner = runner

	if err := sb.Start(context.Background()); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	sb.lastActiveAt = time.Now().Add(-2 * time.Minute)

	cleaned, err := sb.CleanupIfIdle(context.Background(), time.Minute)
	if err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if !cleaned {
		t.Fatal("expected cleanup to happen")
	}
	if got := sb.Info().Status; got != StatusClosed {
		t.Fatalf("status after cleanup = %q, want %q", got, StatusClosed)
	}
	if len(tracer.spans) != 1 {
		t.Fatalf("expected one cleanup span, got %d", len(tracer.spans))
	}
	span := tracer.spans[0]
	if got := span.stringAttr("sandbox.cleanup_reason"); got != "idle" {
		t.Fatalf("cleanup_reason = %q, want %q", got, "idle")
	}
	if got := span.stringAttr("sandbox.cleanup_result"); got != "closed" {
		t.Fatalf("cleanup_result = %q, want %q", got, "closed")
	}
	if got := span.int64Attr("sandbox.memory_limit_bytes"); got != DefaultMemoryLimitBytes {
		t.Fatalf("memory limit attr = %d, want %d", got, DefaultMemoryLimitBytes)
	}
	if got := span.intAttr("sandbox.pid_limit"); got != DefaultProcessLimit {
		t.Fatalf("pid limit attr = %d, want %d", got, DefaultProcessLimit)
	}
}

func TestDockerSandboxCleanupIfIdleDoesNotCleanupBeforeThreshold(t *testing.T) {
	workspace := t.TempDir()
	runner := &fakeDockerRunner{
		lookPath: map[string]string{"docker": "/usr/bin/docker"},
		runFunc: func(ctx context.Context, name string, args ...string) (dockerCommandResult, error) {
			_ = ctx
			_ = name
			return dockerCommandResult{}, nil
		},
	}
	sb := NewDockerSandbox(Config{
		WorkingDir:    workspace,
		ContainerName: "sandbox-test",
	})
	sb.runner = runner

	if err := sb.Start(context.Background()); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	sb.lastActiveAt = time.Now()

	cleaned, err := sb.CleanupIfIdle(context.Background(), time.Minute)
	if err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if cleaned {
		t.Fatal("expected cleanup to be skipped")
	}
	if got := sb.Info().Status; got != StatusRunning {
		t.Fatalf("status after skipped cleanup = %q, want %q", got, StatusRunning)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected only start call, got %d", len(runner.calls))
	}
}

func TestSandboxRuntimeRunsInCurrentRuntime(t *testing.T) {
	sb := &fakeSandbox{
		info: Info{Status: StatusCreated},
	}
	runtime := NewSandboxRuntime(sb)

	out, err := runtime.Run(context.Background(), "tmux", "ls")
	if err != nil {
		t.Fatalf("runtime run failed: %v", err)
	}
	if out != "sandbox-output\n" {
		t.Fatalf("output = %q, want %q", out, "sandbox-output\n")
	}
	if sb.startCalls != 1 {
		t.Fatalf("start calls = %d, want %d", sb.startCalls, 1)
	}
	if len(sb.execCalls) != 1 {
		t.Fatalf("exec calls = %d, want %d", len(sb.execCalls), 1)
	}
	if got := sb.execCalls[0]; len(got) != 2 || got[0] != "tmux" || got[1] != "ls" {
		t.Fatalf("unexpected exec call: %#v", got)
	}
}

func TestSandboxTmuxBackendUsesInjectedRuntimeWithoutDockerField(t *testing.T) {
	sb := &fakeSandbox{
		info: Info{Status: StatusCreated},
	}
	backend := NewSandboxTmuxBackend("/tmp/project", sb)

	if err := backend.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize backend: %v", err)
	}
	if sb.startCalls == 0 {
		t.Fatal("expected sandbox runtime to be prepared")
	}
	if len(sb.execCalls) < 2 {
		t.Fatalf("expected sandbox exec calls for lookpath, got %#v", sb.execCalls)
	}
	if got := sb.execCalls[0]; len(got) < 3 || got[0] != "sh" || got[1] != "-lc" || !strings.Contains(got[2], "tmux") {
		t.Fatalf("expected first exec call to probe tmux in current runtime, got %#v", got)
	}
	if got := sb.execCalls[1]; len(got) < 3 || got[0] != "sh" || got[1] != "-lc" || !strings.Contains(got[2], "bash") {
		t.Fatalf("expected second exec call to probe bash in current runtime, got %#v", got)
	}
}

func TestDefaultTmuxBackendUsesManagerCreatedSandbox(t *testing.T) {
	sb := &fakeSandbox{
		info: Info{Status: StatusCreated},
	}

	backend := NewSandboxTmuxBackend("/tmp/project", sb)
	if err := backend.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize backend: %v", err)
	}
	if sb.startCalls == 0 {
		t.Fatal("expected sandbox runtime to be prepared")
	}
}

func TestSandboxTmuxBackendWithNilSandboxReturnsStableError(t *testing.T) {
	backend := NewSandboxTmuxBackend("/tmp/project", nil)

	err := backend.Initialize(context.Background())
	if err == nil {
		t.Fatal("expected initialize error")
	}
	if !strings.Contains(err.Error(), "sandbox runtime is unavailable") {
		t.Fatalf("expected sandbox unavailable error, got %v", err)
	}
}

func TestHostRuntimeLookPathUsesCurrentEnvironment(t *testing.T) {
	runtime := NewHostRuntime()

	path, err := runtime.LookPath(context.Background(), "sh")
	if err != nil {
		t.Fatalf("look path failed: %v", err)
	}
	if path == "" {
		t.Fatal("expected non-empty sh path")
	}
}

type fakeDockerRunner struct {
	lookPath map[string]string
	runErr   error
	runFunc  func(ctx context.Context, name string, args ...string) (dockerCommandResult, error)
	calls    [][]string
}

func (r *fakeDockerRunner) Run(ctx context.Context, name string, args ...string) (dockerCommandResult, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	if r.runFunc != nil {
		return r.runFunc(ctx, name, args...)
	}
	if r.runErr != nil {
		return dockerCommandResult{}, r.runErr
	}
	return dockerCommandResult{Stdout: "ok\n"}, nil
}

func (r *fakeDockerRunner) LookPath(ctx context.Context, file string) (string, error) {
	_ = ctx
	if path, ok := r.lookPath[file]; ok {
		return path, nil
	}
	return "", fmt.Errorf("missing %s", file)
}

type fakeFactory struct {
	sandbox Sandbox
	configs []Config
}

func (f *fakeFactory) Create(config Config) Sandbox {
	f.configs = append(f.configs, config)
	return f.sandbox
}

type fakeSandbox struct {
	info       Info
	startCalls int
	execCalls  [][]string
}

func (s *fakeSandbox) Start(ctx context.Context) error {
	_ = ctx
	s.startCalls++
	s.info.Status = StatusRunning
	return nil
}

func (s *fakeSandbox) Close(ctx context.Context) error {
	_ = ctx
	s.info.Status = StatusClosed
	return nil
}

func (s *fakeSandbox) Exec(ctx context.Context, command string, args ...string) (string, error) {
	_ = ctx
	call := append([]string{command}, args...)
	s.execCalls = append(s.execCalls, call)
	if command == "sh" && len(args) >= 2 && args[0] == "-lc" {
		script := args[1]
		switch {
		case strings.Contains(script, "tmux"):
			return "/usr/bin/tmux\n", nil
		case strings.Contains(script, "bash"):
			return "/bin/bash\n", nil
		default:
			return "", exec.ErrNotFound
		}
	}
	return "sandbox-output\n", nil
}

func (s *fakeSandbox) CleanupIfIdle(ctx context.Context, idleThreshold time.Duration) (bool, error) {
	_ = ctx
	_ = idleThreshold
	return false, nil
}

func (s *fakeSandbox) Info() Info {
	return s.info
}

type fakeTracer struct {
	spans []*fakeSpan
}

func (t *fakeTracer) StartSpan(ctx context.Context, name string, opts ...observability.SpanOption) (context.Context, observability.Span) {
	_ = opts
	span := &fakeSpan{name: name}
	t.spans = append(t.spans, span)
	return ctx, span
}

type fakeSpanEvent struct {
	name  string
	attrs map[string]any
}

type fakeSpan struct {
	name   string
	attrs  map[string]any
	events []fakeSpanEvent
	status observability.SpanStatus
}

func (s *fakeSpan) End() {}

func (s *fakeSpan) SetStatus(status observability.SpanStatus, description string) {
	_ = description
	s.status = status
}

func (s *fakeSpan) RecordError(err error, status observability.SpanStatus) {
	_ = err
	s.status = status
}

func (s *fakeSpan) AddEvent(name string, attrs ...observability.Attribute) {
	s.events = append(s.events, fakeSpanEvent{name: name, attrs: attrsToMap(attrs)})
}

func (s *fakeSpan) SetAttributes(attrs ...observability.Attribute) {
	if s.attrs == nil {
		s.attrs = make(map[string]any)
	}
	for key, value := range attrsToMap(attrs) {
		s.attrs[key] = value
	}
}

func (s *fakeSpan) TraceID() string { return "" }

func (s *fakeSpan) SpanID() string { return "" }

func (s *fakeSpan) boolAttr(key string) bool {
	value, _ := s.attrs[key].(bool)
	return value
}

func (s *fakeSpan) stringAttr(key string) string {
	value, _ := s.attrs[key].(string)
	return value
}

func (s *fakeSpan) intAttr(key string) int {
	value, _ := s.attrs[key].(int)
	return value
}

func (s *fakeSpan) int64Attr(key string) int64 {
	value, _ := s.attrs[key].(int64)
	return value
}

func attrsToMap(attrs []observability.Attribute) map[string]any {
	out := make(map[string]any, len(attrs))
	for _, attr := range attrs {
		out[attr.Key] = attr.Value
	}
	return out
}

func installFakeTracer() func() {
	return replaceTracer(&fakeTracer{})
}

func replaceTracer(tracer observability.Tracer) func() {
	previous := tracerForSandbox
	tracerForSandbox = func() observability.Tracer { return tracer }
	return func() {
		tracerForSandbox = previous
	}
}
