package terminal

import (
	"context"
	"errors"
)

var errPTYBackendNotImplemented = errors.New("pty backend is not implemented yet")

// TerminalBackend 定义终端会话后端需要实现的统一能力。
type TerminalBackend interface {
	Initialize(ctx context.Context) error
	Close(ctx context.Context) error
	SendKeys(ctx context.Context, text string, enter bool) error
	ReadScreen(ctx context.Context) (string, error)
	ClearScreen(ctx context.Context) error
	Interrupt(ctx context.Context) (bool, error)
	IsRunning(ctx context.Context) (bool, error)
}

// terminalBackendMetadata 定义终端后端可选暴露的元信息能力。
type terminalBackendMetadata interface {
	PanePID(ctx context.Context) (*int, error)
	CurrentWorkingDir(ctx context.Context) (string, error)
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
func (b *PTYBackend) SendKeys(ctx context.Context, text string, enter bool) error {
	return errPTYBackendNotImplemented
}

// ReadScreen 读取 PTY 后端当前屏幕内容。
func (b *PTYBackend) ReadScreen(ctx context.Context) (string, error) {
	return "", errPTYBackendNotImplemented
}

// ClearScreen 清空 PTY 后端当前屏幕和历史。
func (b *PTYBackend) ClearScreen(ctx context.Context) error {
	return errPTYBackendNotImplemented
}

// Interrupt 向 PTY 后端发送中断信号。
func (b *PTYBackend) Interrupt(ctx context.Context) (bool, error) {
	return false, errPTYBackendNotImplemented
}

// IsRunning 检测 PTY 后端是否有前台命令正在执行。
func (b *PTYBackend) IsRunning(ctx context.Context) (bool, error) {
	return false, errPTYBackendNotImplemented
}
