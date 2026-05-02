package sandbox

import (
	"context"
	"fmt"

	terminalpkg "github.com/wen/opentalon/internal/tool/terminal"
)

type unavailableRuntime struct {
	err error
}

func (r unavailableRuntime) Prepare(ctx context.Context) error {
	_ = ctx
	return r.err
}

func (r unavailableRuntime) Close(ctx context.Context) error {
	_ = ctx
	return nil
}

func (r unavailableRuntime) Run(ctx context.Context, name string, args ...string) (string, error) {
	_ = ctx
	_ = name
	_ = args
	return "", r.err
}

func (r unavailableRuntime) LookPath(ctx context.Context, file string) (string, error) {
	_ = ctx
	_ = file
	return "", r.err
}

// NewSandboxTmuxBackend 创建绑定 sandbox runtime 的 tmux backend。
func NewSandboxTmuxBackend(workingDir string, sb Sandbox) terminalpkg.TerminalBackend {
	runtime := NewSandboxRuntime(sb)
	if runtime == nil {
		runtime = unavailableRuntime{err: fmt.Errorf("sandbox runtime is unavailable")}
	}
	runtimeWorkingDir := DefaultContainerWorkDir
	if sb != nil {
		info := sb.Info()
		if info.ContainerWorkDir != "" {
			runtimeWorkingDir = info.ContainerWorkDir
		}
		if info.HostWorkingDir != "" {
			workingDir = info.HostWorkingDir
		}
	}
	return terminalpkg.NewTmuxBackendWithRuntimeLayout(workingDir, runtimeWorkingDir, runtime)
}

// NewHostTmuxBackend 创建绑定宿主机 runtime 的 tmux backend。
func NewHostTmuxBackend(workingDir string) terminalpkg.TerminalBackend {
	return terminalpkg.NewTmuxBackendWithRunner(workingDir, NewHostRuntime())
}

// PrepareTerminalRuntime 预热当前运行环境，供上层装配时显式调用。
func PrepareTerminalRuntime(ctx context.Context, runtime Runtime) error {
	if runtime == nil {
		return nil
	}
	return runtime.Prepare(ctx)
}
