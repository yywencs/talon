package terminal

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
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
	workingDir  string
	session     string
	shellPath   string
	runner      tmuxCommandRunner
	initialized bool

	mu sync.Mutex
}

// NewTmuxBackend 创建 tmux 会话后端。
func NewTmuxBackend(workingDir string) *TmuxBackend {
	return &TmuxBackend{
		workingDir: workingDir,
		session:    "opentalon-" + uuid.NewString(),
		runner:     &execTmuxCommandRunner{},
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
	if b.initialized {
		return nil
	}

	args := []string{"new-session", "-d", "-s", b.session}
	if b.workingDir != "" {
		args = append(args, "-c", b.workingDir)
	}
	args = append(args, b.shellPath+" --noprofile --norc -i")
	if _, err := b.runTmuxLocked(ctx, args...); err != nil {
		return fmt.Errorf("failed to create tmux session %q: %w", b.session, err)
	}
	if _, err := b.runTmuxLocked(ctx, "set-option", "-t", b.session, "history-limit", "200000"); err != nil {
		return fmt.Errorf("failed to configure tmux history limit: %w", err)
	}
	if err := b.sendKeysLocked(ctx, "export PS1='' PROMPT_COMMAND=; stty -echo", true); err != nil {
		return fmt.Errorf("failed to initialize tmux shell: %w", err)
	}
	if err := b.clearScreenLocked(ctx); err != nil {
		return fmt.Errorf("failed to clear tmux screen: %w", err)
	}
	b.initialized = true
	return nil
}

// Close 关闭 tmux session 并清理资源。
func (b *TmuxBackend) Close(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.initialized {
		return nil
	}
	if _, err := b.runTmuxLocked(ctx, "kill-session", "-t", b.session); err != nil {
		if !b.initialized {
			b.initialized = false
			return nil
		}
		return fmt.Errorf("failed to kill tmux session %q: %w", b.session, err)
	}
	b.initialized = false
	return nil
}

// SendKeys 向 tmux session 发送文本或按键序列。
func (b *TmuxBackend) SendKeys(ctx context.Context, text string, enter bool) error {
	if err := b.Initialize(ctx); err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sendKeysLocked(ctx, text, enter)
}

// ReadScreen 读取 tmux 当前屏幕及历史输出。
func (b *TmuxBackend) ReadScreen(ctx context.Context) (string, error) {
	if err := b.Initialize(ctx); err != nil {
		return "", err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	out, err := b.runTmuxLocked(ctx, "capture-pane", "-p", "-J", "-t", b.session, "-S", "-")
	if err != nil {
		return "", fmt.Errorf("failed to capture tmux pane: %w", err)
	}
	return out, nil
}

// ClearScreen 清理 tmux 屏幕和历史输出。
func (b *TmuxBackend) ClearScreen(ctx context.Context) error {
	if err := b.Initialize(ctx); err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	return b.clearScreenLocked(ctx)
}

// Interrupt 向 tmux 前台进程发送 Ctrl+C。
func (b *TmuxBackend) Interrupt(ctx context.Context) (bool, error) {
	if err := b.Initialize(ctx); err != nil {
		return false, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if _, err := b.runTmuxLocked(ctx, "send-keys", "-t", b.session, "C-c"); err != nil {
		return false, fmt.Errorf("failed to interrupt tmux session: %w", err)
	}
	return true, nil
}

// IsRunning 检测 tmux 前台是否仍有非 shell 进程在运行。
func (b *TmuxBackend) IsRunning(ctx context.Context) (bool, error) {
	if err := b.Initialize(ctx); err != nil {
		return false, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	out, err := b.runTmuxLocked(ctx, "display-message", "-p", "-t", b.session, "#{pane_current_command}")
	if err != nil {
		return false, fmt.Errorf("failed to inspect tmux current command: %w", err)
	}
	commandName := strings.TrimSpace(out)
	if commandName == "" {
		return false, nil
	}
	return commandName != "bash", nil
}

// PanePID 返回 tmux pane 对应 shell 的进程号。
func (b *TmuxBackend) PanePID(ctx context.Context) (*int, error) {
	if err := b.Initialize(ctx); err != nil {
		return nil, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	out, err := b.runTmuxLocked(ctx, "display-message", "-p", "-t", b.session, "#{pane_pid}")
	if err != nil {
		return nil, fmt.Errorf("failed to inspect tmux pane pid: %w", err)
	}
	pidValue, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return nil, fmt.Errorf("failed to parse tmux pane pid %q: %w", strings.TrimSpace(out), err)
	}
	return intPtr(pidValue), nil
}

// CurrentWorkingDir 返回 tmux pane 当前工作目录。
func (b *TmuxBackend) CurrentWorkingDir(ctx context.Context) (string, error) {
	if err := b.Initialize(ctx); err != nil {
		return "", err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	out, err := b.runTmuxLocked(ctx, "display-message", "-p", "-t", b.session, "#{pane_current_path}")
	if err != nil {
		return "", fmt.Errorf("failed to inspect tmux working directory: %w", err)
	}
	return strings.TrimSpace(out), nil
}

func (b *TmuxBackend) clearScreenLocked(ctx context.Context) error {
	if _, err := b.runTmuxLocked(ctx, "send-keys", "-t", b.session, "C-l"); err != nil {
		return fmt.Errorf("failed to clear tmux screen: %w", err)
	}
	if _, err := b.runTmuxLocked(ctx, "clear-history", "-t", b.session); err != nil {
		return fmt.Errorf("failed to clear tmux history: %w", err)
	}
	return nil
}

func (b *TmuxBackend) sendKeysLocked(ctx context.Context, text string, enter bool) error {
	if text != "" {
		if _, err := b.runTmuxLocked(ctx, "send-keys", "-t", b.session, "-l", text); err != nil {
			return fmt.Errorf("failed to send keys to tmux session: %w", err)
		}
	}
	if enter {
		if _, err := b.runTmuxLocked(ctx, "send-keys", "-t", b.session, "Enter"); err != nil {
			return fmt.Errorf("failed to send enter to tmux session: %w", err)
		}
	}
	return nil
}

func (b *TmuxBackend) runTmuxLocked(ctx context.Context, args ...string) (string, error) {
	out, err := b.runner.Run(ctx, args...)
	if err != nil && isTmuxSessionMissing(out) {
		b.initialized = false
	}
	return out, err
}

func isTmuxSessionMissing(output string) bool {
	return strings.Contains(output, "can't find session") || strings.Contains(output, "no server running")
}
