package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Runtime 定义“当前运行环境”对外暴露的最小执行能力。
type Runtime interface {
	Prepare(ctx context.Context) error
	Close(ctx context.Context) error
	Run(ctx context.Context, name string, args ...string) (string, error)
	LookPath(ctx context.Context, file string) (string, error)
}

type execRuntime struct{}

// NewHostRuntime 创建宿主机运行环境。
func NewHostRuntime() Runtime {
	return &execRuntime{}
}

func (r *execRuntime) Prepare(ctx context.Context) error {
	_ = ctx
	return nil
}

func (r *execRuntime) Close(ctx context.Context) error {
	_ = ctx
	return nil
}

func (r *execRuntime) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (r *execRuntime) LookPath(ctx context.Context, file string) (string, error) {
	_ = ctx
	return exec.LookPath(file)
}

type sandboxRuntime struct {
	sandbox Sandbox
}

// NewSandboxRuntime 创建绑定指定 sandbox 实例的运行环境。
func NewSandboxRuntime(sb Sandbox) Runtime {
	if sb == nil {
		return nil
	}
	return &sandboxRuntime{sandbox: sb}
}

func (r *sandboxRuntime) Prepare(ctx context.Context) error {
	return r.sandbox.Start(ctx)
}

func (r *sandboxRuntime) Close(ctx context.Context) error {
	return r.sandbox.Close(ctx)
}

func (r *sandboxRuntime) Run(ctx context.Context, name string, args ...string) (string, error) {
	if err := r.Prepare(ctx); err != nil {
		return "", err
	}
	return r.sandbox.Exec(ctx, name, args...)
}

func (r *sandboxRuntime) LookPath(ctx context.Context, file string) (string, error) {
	if err := r.Prepare(ctx); err != nil {
		return "", err
	}
	command := "command -v -- " + shellQuote(file)
	out, err := r.sandbox.Exec(ctx, "sh", "-lc", command)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// MustRuntime 表示运行环境不能为空的构造约束。
func MustRuntime(runtime Runtime) (Runtime, error) {
	if runtime == nil {
		return nil, fmt.Errorf("runtime is nil")
	}
	return runtime, nil
}
