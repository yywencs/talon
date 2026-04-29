package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestManagerCreateReturnsPlaceholderSandbox(t *testing.T) {
	manager := NewManager(nil)

	sb := manager.Create(Config{WorkingDir: "/tmp/project"})
	if sb == nil {
		t.Fatal("expected sandbox instance")
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
	if !strings.Contains(strings.Join(runner.calls[0], " "), "run -d --rm --name sandbox-test -w /workspace -v /tmp/project:/workspace") {
		t.Fatalf("unexpected docker run args: %v", runner.calls[0])
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
	calls    [][]string
}

func (r *fakeDockerRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	_ = ctx
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	if r.runErr != nil {
		return "", r.runErr
	}
	return "ok\n", nil
}

func (r *fakeDockerRunner) LookPath(ctx context.Context, file string) (string, error) {
	_ = ctx
	if path, ok := r.lookPath[file]; ok {
		return path, nil
	}
	return "", fmt.Errorf("missing %s", file)
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

func (s *fakeSandbox) Info() Info {
	return s.info
}
