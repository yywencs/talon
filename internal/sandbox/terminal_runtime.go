package sandbox

import (
	"context"

	terminalpkg "github.com/wen/opentalon/internal/tool/terminal"
)

// NewSandboxTmuxBackend 创建绑定 sandbox runtime 的 tmux backend。
func NewSandboxTmuxBackend(workingDir string, sb Sandbox) terminalpkg.TerminalBackend {
	runtime := NewSandboxRuntime(sb)
	if runtime == nil {
		return terminalpkg.NewTmuxBackend(workingDir)
	}
	return terminalpkg.NewTmuxBackendWithRunner(workingDir, runtime)
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
