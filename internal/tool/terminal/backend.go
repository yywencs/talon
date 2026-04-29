package terminal

import (
	"context"
	"errors"
)

var errPTYBackendNotImplemented = errors.New("pty backend is not implemented yet")

// CommandRunner 定义 terminal 在当前运行环境中执行命令所需的最小能力。
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
	LookPath(ctx context.Context, file string) (string, error)
}

// TerminalBackend 定义终端会话后端需要实现的统一能力。
type TerminalBackend interface {
	Initialize(ctx context.Context) error
	Close(ctx context.Context) error
	SendKeys(ctx context.Context, paneID, text string, enter bool) error
	ReadScreen(ctx context.Context, paneID string) (string, error)
	ClearScreen(ctx context.Context, paneID string) error
	Interrupt(ctx context.Context, paneID string) (bool, error)
	IsRunning(ctx context.Context, paneID string) (bool, error)
}

// terminalBackendCommandLifecycle 定义终端后端可选暴露的命令生命周期管理能力。
type terminalBackendCommandLifecycle interface {
	PrepareCommand(ctx context.Context, paneID string) error
	CompleteCommand(ctx context.Context, paneID string) error
	InvalidateCommand(ctx context.Context, paneID string) error
	ResetPane(ctx context.Context, paneID string) error
}

// terminalBackendMetadata 定义终端后端可选暴露的元信息能力。
type terminalBackendMetadata interface {
	PanePID(ctx context.Context, paneID string) (*int, error)
	CurrentWorkingDir(ctx context.Context, paneID string) (string, error)
}

// PTYBackend 表示 PTY 会话后端占位实现。
type PTYBackend struct {
	workingDir string
}

// TODO： NewPTYBackend 创建 PTY 会话后端占位实现。
func NewPTYBackend(workingDir string) *PTYBackend {
	return &PTYBackend{workingDir: workingDir}
}

// Initialize 初始化 PTY 后端。
func (b *PTYBackend) Initialize(ctx context.Context) error {
	return errPTYBackendNotImplemented
}

// Close 关闭 PTY 后端并清理资源。
func (b *PTYBackend) Close(ctx context.Context) error {
	return errPTYBackendNotImplemented
}

// SendKeys 向 PTY 后端发送输入。
func (b *PTYBackend) SendKeys(ctx context.Context, paneID, text string, enter bool) error {
	return errPTYBackendNotImplemented
}

// ReadScreen 读取 PTY 后端当前屏幕内容。
func (b *PTYBackend) ReadScreen(ctx context.Context, paneID string) (string, error) {
	return "", errPTYBackendNotImplemented
}

// ClearScreen 清空 PTY 后端当前屏幕和历史。
func (b *PTYBackend) ClearScreen(ctx context.Context, paneID string) error {
	return errPTYBackendNotImplemented
}

// Interrupt 向 PTY 后端发送中断信号。
func (b *PTYBackend) Interrupt(ctx context.Context, paneID string) (bool, error) {
	return false, errPTYBackendNotImplemented
}

// IsRunning 检测 PTY 后端是否有前台命令正在执行。
func (b *PTYBackend) IsRunning(ctx context.Context, paneID string) (bool, error) {
	return false, errPTYBackendNotImplemented
}
