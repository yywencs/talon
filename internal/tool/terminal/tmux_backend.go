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
	LookPath(ctx context.Context, file string) (string, error)
}

type tmuxCommandRunnerPreparer interface {
	Prepare(ctx context.Context) error
}

type tmuxCommandRunnerCloser interface {
	Close(ctx context.Context) error
}

type execTmuxCommandRunner struct{}

func (r *execTmuxCommandRunner) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "tmux", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (r *execTmuxCommandRunner) LookPath(ctx context.Context, file string) (string, error) {
	_ = ctx
	return exec.LookPath(file)
}

type commandRunnerTmuxAdapter struct {
	runner CommandRunner
}

func (r *commandRunnerTmuxAdapter) Run(ctx context.Context, args ...string) (string, error) {
	return r.runner.Run(ctx, "tmux", args...)
}

func (r *commandRunnerTmuxAdapter) LookPath(ctx context.Context, file string) (string, error) {
	return r.runner.LookPath(ctx, file)
}

func (r *commandRunnerTmuxAdapter) Prepare(ctx context.Context) error {
	preparer, ok := r.runner.(interface {
		Prepare(context.Context) error
	})
	if !ok {
		return nil
	}
	return preparer.Prepare(ctx)
}

func (r *commandRunnerTmuxAdapter) Close(ctx context.Context) error {
	closer, ok := r.runner.(interface {
		Close(context.Context) error
	})
	if !ok {
		return nil
	}
	return closer.Close(ctx)
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

// NewTmuxBackendWithRunner 创建绑定当前运行环境命令执行器的 tmux 会话后端。
func NewTmuxBackendWithRunner(workingDir string, runner CommandRunner) *TmuxBackend {
	backend := NewTmuxBackend(workingDir)
	if runner != nil {
		backend.runner = &commandRunnerTmuxAdapter{runner: runner}
	}
	return backend
}

// Initialize 初始化 tmux session。
func (b *TmuxBackend) Initialize(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if preparer, ok := b.runner.(tmuxCommandRunnerPreparer); ok {
		if err := preparer.Prepare(ctx); err != nil {
			return err
		}
	}
	if _, err := b.runner.LookPath(ctx, "tmux"); err != nil {
		return fmt.Errorf("tmux is not available: %w", err)
	}
	if b.shellPath == "" {
		shellPath, err := b.runner.LookPath(ctx, "bash")
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
	if err := b.closeSessionLocked(ctx); err != nil {
		return err
	}
	if closer, ok := b.runner.(tmuxCommandRunnerCloser); ok {
		return closer.Close(ctx)
	}
	return nil
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

type tmuxPaneHandle struct {
	PaneID string
}

func (p *tmuxPaneHandle) target() string {
	if p == nil {
		return ""
	}
	return p.PaneID
}

// PrepareCommand 为逻辑 pane 确保固定绑定的可执行 pane。
func (b *TmuxBackend) PrepareCommand(ctx context.Context, paneID string) error {
	if err := b.Initialize(ctx); err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	return b.prepareCommandLocked(ctx, paneID)
}

// CompleteCommand 在命令链路结束后清理当前 pane 的临时执行状态。
func (b *TmuxBackend) CompleteCommand(ctx context.Context, paneID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.initialized {
		return nil
	}
	return b.completeCommandLocked(ctx, paneID)
}

// InvalidateCommand 在命令链路异常后销毁当前逻辑 pane 的固定绑定。
func (b *TmuxBackend) InvalidateCommand(ctx context.Context, paneID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.initialized {
		return nil
	}
	return b.invalidateCommandLocked(ctx, paneID)
}

// ResetPane 重置某个逻辑 pane 绑定的交互链路。
func (b *TmuxBackend) ResetPane(ctx context.Context, paneID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.initialized {
		return nil
	}
	return b.resetPaneLocked(ctx, paneID)
}

func (b *TmuxBackend) initializeSessionLocked(ctx context.Context) error {
	if b.paneBindings == nil {
		b.paneBindings = make(map[string]*tmuxPaneHandle)
	}
	if b.initialized {
		return nil
	}

	args := []string{
		"new-session", "-d", "-P", "-F", "#{pane_id}",
		"-s", b.session,
	}
	if b.workingDir != "" {
		args = append(args, "-c", b.workingDir)
	}
	args = append(args, b.shellCommand())
	out, err := b.runTmuxLocked(ctx, args...)
	if err != nil {
		return fmt.Errorf("failed to create tmux session %q: %w", b.session, err)
	}

	rootPane, err := parseTmuxPaneHandle(out)
	if err != nil {
		return fmt.Errorf("failed to parse tmux root pane: %w", err)
	}
	if _, err := b.runTmuxLocked(ctx, "set-option", "-t", b.session, "history-limit", "200000"); err != nil {
		return fmt.Errorf("failed to configure tmux history limit: %w", err)
	}
	if err := b.bootstrapPaneLocked(ctx, rootPane); err != nil {
		_ = b.killPaneLocked(ctx, rootPane.PaneID)
		return err
	}

	b.initialized = true
	b.paneBindings = make(map[string]*tmuxPaneHandle)
	b.sessionRoot = rootPane
	return nil
}

func (b *TmuxBackend) closeSessionLocked(ctx context.Context) error {
	if !b.initialized {
		b.resetSessionStateLocked()
		return nil
	}
	if out, err := b.runTmuxLocked(ctx, "kill-session", "-t", b.session); err != nil {
		if isTmuxSessionMissing(out) {
			b.resetSessionStateLocked()
			return nil
		}
		return fmt.Errorf("failed to kill tmux session %q: %w", b.session, err)
	}
	b.resetSessionStateLocked()
	return nil
}

func (b *TmuxBackend) prepareCommandLocked(ctx context.Context, paneID string) error {
	if b.paneBindings == nil {
		b.paneBindings = make(map[string]*tmuxPaneHandle)
	}
	if _, exists := b.paneBindings[paneID]; exists {
		return nil
	}

	pane, err := b.bindPaneLocked(ctx)
	if err != nil {
		return err
	}
	b.paneBindings[paneID] = pane
	return nil
}

func (b *TmuxBackend) completeCommandLocked(_ context.Context, paneID string) error {
	if _, ok := b.paneBindings[paneID]; !ok {
		return nil
	}
	return nil
}

func (b *TmuxBackend) invalidateCommandLocked(ctx context.Context, paneID string) error {
	pane, ok := b.paneBindings[paneID]
	if !ok {
		return nil
	}
	delete(b.paneBindings, paneID)
	if pane == b.sessionRoot {
		b.sessionRoot = nil
	}
	return b.killPaneLocked(ctx, pane.PaneID)
}

func (b *TmuxBackend) resetPaneLocked(ctx context.Context, paneID string) error {
	return b.invalidateCommandLocked(ctx, paneID)
}

func (b *TmuxBackend) bindPaneLocked(ctx context.Context) (*tmuxPaneHandle, error) {
	if b.sessionRoot != nil {
		pane := b.sessionRoot
		b.sessionRoot = nil
		return pane, nil
	}
	return b.createPaneLocked(ctx)
}

func (b *TmuxBackend) createPaneLocked(ctx context.Context) (*tmuxPaneHandle, error) {
	args := []string{
		"new-window", "-d", "-P", "-F", "#{pane_id}",
		"-t", b.session,
	}
	if b.workingDir != "" {
		args = append(args, "-c", b.workingDir)
	}
	args = append(args, b.shellCommand())
	out, err := b.runTmuxLocked(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to create tmux fixed pane: %w", err)
	}

	pane, err := parseTmuxPaneHandle(out)
	if err != nil {
		return nil, fmt.Errorf("failed to parse tmux fixed pane: %w", err)
	}
	if err := b.bootstrapPaneLocked(ctx, pane); err != nil {
		_ = b.killPaneLocked(ctx, pane.PaneID)
		return nil, err
	}
	return pane, nil
}

func (b *TmuxBackend) bootstrapPaneLocked(ctx context.Context, pane *tmuxPaneHandle) error {
	if err := b.sendKeysToPaneLocked(ctx, pane, "export PS1='' PROMPT_COMMAND=; stty -echo", true); err != nil {
		return fmt.Errorf("failed to initialize tmux shell: %w", err)
	}
	if err := b.clearPaneLocked(ctx, pane); err != nil {
		return fmt.Errorf("failed to clear tmux screen: %w", err)
	}
	return nil
}

func (b *TmuxBackend) sendKeysToCurrentPaneLocked(ctx context.Context, paneID, text string, enter bool) error {
	pane, err := b.requireCurrentPaneLocked(paneID)
	if err != nil {
		return err
	}
	return b.sendKeysToPaneLocked(ctx, pane, text, enter)
}

func (b *TmuxBackend) sendKeysToPaneLocked(ctx context.Context, pane *tmuxPaneHandle, text string, enter bool) error {
	if text != "" {
		if _, err := b.runTmuxLocked(ctx, "send-keys", "-t", pane.target(), "-l", text); err != nil {
			return fmt.Errorf("failed to send keys to tmux pane %q: %w", pane.PaneID, err)
		}
	}
	if enter {
		if _, err := b.runTmuxLocked(ctx, "send-keys", "-t", pane.target(), "Enter"); err != nil {
			return fmt.Errorf("failed to send enter to tmux pane %q: %w", pane.PaneID, err)
		}
	}
	return nil
}

func (b *TmuxBackend) readCurrentPaneLocked(ctx context.Context, paneID string) (string, error) {
	pane, err := b.currentMetadataPaneLocked(paneID)
	if err != nil {
		return "", err
	}
	out, err := b.runTmuxLocked(ctx, "capture-pane", "-p", "-J", "-t", pane.target(), "-S", "-")
	if err != nil {
		return "", fmt.Errorf("failed to capture tmux pane %q: %w", pane.PaneID, err)
	}
	return out, nil
}

func (b *TmuxBackend) clearCurrentPaneLocked(ctx context.Context, paneID string) error {
	pane, err := b.currentMetadataPaneLocked(paneID)
	if err != nil {
		return err
	}
	return b.clearPaneLocked(ctx, pane)
}

func (b *TmuxBackend) clearPaneLocked(ctx context.Context, pane *tmuxPaneHandle) error {
	if _, err := b.runTmuxLocked(ctx, "send-keys", "-t", pane.target(), "C-l"); err != nil {
		return fmt.Errorf("failed to clear tmux screen for pane %q: %w", pane.PaneID, err)
	}
	if _, err := b.runTmuxLocked(ctx, "clear-history", "-t", pane.target()); err != nil {
		return fmt.Errorf("failed to clear tmux history for pane %q: %w", pane.PaneID, err)
	}
	return nil
}

func (b *TmuxBackend) interruptCurrentPaneLocked(ctx context.Context, paneID string) (bool, error) {
	pane, err := b.requireCurrentPaneLocked(paneID)
	if err != nil {
		return false, err
	}
	if _, err := b.runTmuxLocked(ctx, "send-keys", "-t", pane.target(), "C-c"); err != nil {
		return false, fmt.Errorf("failed to interrupt tmux pane %q: %w", pane.PaneID, err)
	}
	return true, nil
}

func (b *TmuxBackend) isCurrentPaneRunningLocked(ctx context.Context, paneID string) (bool, error) {
	pane, err := b.requireCurrentPaneLocked(paneID)
	if err != nil {
		return false, err
	}
	out, err := b.runTmuxLocked(ctx, "display-message", "-p", "-t", pane.target(), "#{pane_current_command}")
	if err != nil {
		return false, fmt.Errorf("failed to inspect tmux current command for pane %q: %w", pane.PaneID, err)
	}
	commandName := strings.TrimSpace(out)
	if commandName == "" {
		return false, nil
	}
	return commandName != "bash", nil
}

func (b *TmuxBackend) currentPanePIDLocked(ctx context.Context, paneID string) (*int, error) {
	pane, err := b.currentMetadataPaneLocked(paneID)
	if err != nil {
		return nil, nil
	}
	out, err := b.runTmuxLocked(ctx, "display-message", "-p", "-t", pane.target(), "#{pane_pid}")
	if err != nil {
		return nil, fmt.Errorf("failed to inspect tmux pane pid: %w", err)
	}
	pidValue, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return nil, fmt.Errorf("failed to parse tmux pane pid %q: %w", strings.TrimSpace(out), err)
	}
	return intPtr(pidValue), nil
}

func (b *TmuxBackend) currentPaneWorkingDirLocked(ctx context.Context, paneID string) (string, error) {
	pane, err := b.currentMetadataPaneLocked(paneID)
	if err != nil {
		return "", nil
	}
	out, err := b.runTmuxLocked(ctx, "display-message", "-p", "-t", pane.target(), "#{pane_current_path}")
	if err != nil {
		return "", fmt.Errorf("failed to inspect tmux working directory: %w", err)
	}
	return strings.TrimSpace(out), nil
}

func (b *TmuxBackend) currentMetadataPaneLocked(paneID string) (*tmuxPaneHandle, error) {
	if pane, ok := b.paneBindings[paneID]; ok {
		return pane, nil
	}
	return nil, fmt.Errorf("tmux pane for pane_id %q is not bound", paneID)
}

func (b *TmuxBackend) requireCurrentPaneLocked(paneID string) (*tmuxPaneHandle, error) {
	pane, ok := b.paneBindings[paneID]
	if !ok {
		return nil, fmt.Errorf("tmux pane for pane_id %q is not bound", paneID)
	}
	return pane, nil
}

func (b *TmuxBackend) killPaneLocked(ctx context.Context, paneID string) error {
	if paneID == "" || !b.initialized {
		return nil
	}
	if out, err := b.runTmuxLocked(ctx, "kill-pane", "-t", paneID); err != nil {
		if isTmuxSessionMissing(out) {
			return nil
		}
		return fmt.Errorf("failed to kill tmux pane %q: %w", paneID, err)
	}
	return nil
}

func (b *TmuxBackend) resetSessionStateLocked() {
	b.initialized = false
	b.sessionRoot = nil
	b.paneBindings = make(map[string]*tmuxPaneHandle)
}

func (b *TmuxBackend) shellCommand() string {
	return b.shellPath + " --noprofile --norc -i"
}

func parseTmuxPaneHandle(output string) (*tmuxPaneHandle, error) {
	paneID := strings.TrimSpace(output)
	if paneID == "" {
		return nil, fmt.Errorf("unexpected tmux pane output %q", strings.TrimSpace(output))
	}
	return &tmuxPaneHandle{
		PaneID: paneID,
	}, nil
}

func isTmuxSessionMissing(output string) bool {
	return strings.Contains(output, "can't find session") || strings.Contains(output, "no server running")
}
