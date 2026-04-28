package terminal

import (
	"context"
	"fmt"
	"os/exec"
	"sync"

	"github.com/google/uuid"
)

type tmuxCommandRunner interface {
	Run(ctx context.Context, args ...string) (string, error)
	LookPath(file string) (string, error)
}

type execTmuxCommandRunner struct{}

func (r *execTmuxCommandRunner) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "tmux", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (r *execTmuxCommandRunner) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

// TmuxBackend 表示基于 tmux session 的终端后端。
type TmuxBackend struct {
	workingDir   string
	session      string
	shellPath    string
	runner       tmuxCommandRunner
	initialized  bool
	sessionRoot  *tmuxPaneHandle
	paneBindings map[string]*tmuxPaneHandle

	mu sync.Mutex
}

// NewTmuxBackend 创建 tmux 会话后端。
func NewTmuxBackend(workingDir string) *TmuxBackend {
	return &TmuxBackend{
		workingDir:   workingDir,
		session:      "opentalon-" + uuid.NewString(),
		runner:       &execTmuxCommandRunner{},
		paneBindings: make(map[string]*tmuxPaneHandle),
	}
}

// Initialize 初始化 tmux session。
func (b *TmuxBackend) Initialize(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, err := b.runner.LookPath("tmux"); err != nil {
		return fmt.Errorf("tmux is not available: %w", err)
	}
	if b.shellPath == "" {
		shellPath, err := b.runner.LookPath("bash")
		if err != nil {
			return fmt.Errorf("bash is not available: %w", err)
		}
		b.shellPath = shellPath
	}
	return b.initializeSessionLocked(ctx)
}

// Close 关闭 tmux session 并清理资源。
func (b *TmuxBackend) Close(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closeSessionLocked(ctx)
}

// SendKeys 向 tmux session 发送文本或按键序列。
func (b *TmuxBackend) SendKeys(ctx context.Context, paneID, text string, enter bool) error {
	if err := b.Initialize(ctx); err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sendKeysToCurrentPaneLocked(ctx, paneID, text, enter)
}

// ReadScreen 读取 tmux 当前屏幕及历史输出。
func (b *TmuxBackend) ReadScreen(ctx context.Context, paneID string) (string, error) {
	if err := b.Initialize(ctx); err != nil {
		return "", err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	return b.readCurrentPaneLocked(ctx, paneID)
}

// ClearScreen 清理 tmux 屏幕和历史输出。
func (b *TmuxBackend) ClearScreen(ctx context.Context, paneID string) error {
	if err := b.Initialize(ctx); err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	return b.clearCurrentPaneLocked(ctx, paneID)
}

// Interrupt 向 tmux 前台进程发送 Ctrl+C。
func (b *TmuxBackend) Interrupt(ctx context.Context, paneID string) (bool, error) {
	if err := b.Initialize(ctx); err != nil {
		return false, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	return b.interruptCurrentPaneLocked(ctx, paneID)
}

// IsRunning 检测 tmux 前台是否仍有非 shell 进程在运行。
func (b *TmuxBackend) IsRunning(ctx context.Context, paneID string) (bool, error) {
	if err := b.Initialize(ctx); err != nil {
		return false, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	return b.isCurrentPaneRunningLocked(ctx, paneID)
}

// PanePID 返回 tmux pane 对应 shell 的进程号。
func (b *TmuxBackend) PanePID(ctx context.Context, paneID string) (*int, error) {
	if err := b.Initialize(ctx); err != nil {
		return nil, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	return b.currentPanePIDLocked(ctx, paneID)
}

// CurrentWorkingDir 返回 tmux pane 当前工作目录。
func (b *TmuxBackend) CurrentWorkingDir(ctx context.Context, paneID string) (string, error) {
	if err := b.Initialize(ctx); err != nil {
		return "", err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	return b.currentPaneWorkingDirLocked(ctx, paneID)
}

func (b *TmuxBackend) runTmuxLocked(ctx context.Context, args ...string) (string, error) {
	out, err := b.runner.Run(ctx, args...)
	if err != nil && isTmuxSessionMissing(out) {
		b.resetSessionStateLocked()
	}
	return out, err
}
