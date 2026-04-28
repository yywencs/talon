package terminal

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

const defaultTmuxPanePoolSize = 5

type tmuxPaneHandle struct {
	WindowID string
	PaneID   string
}

func (p *tmuxPaneHandle) target() string {
	if p == nil {
		return ""
	}
	return p.PaneID
}

// PrepareCommand 为下一条普通命令分配可执行 pane。
func (b *TmuxBackend) PrepareCommand(ctx context.Context) error {
	if err := b.Initialize(ctx); err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	return b.prepareCommandLocked(ctx)
}

// CompleteCommand 在命令链路结束后清理并回收当前 pane。
func (b *TmuxBackend) CompleteCommand(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.initialized {
		return nil
	}
	return b.completeCommandLocked(ctx)
}

// InvalidateCommand 在命令链路异常后丢弃当前 pane。
func (b *TmuxBackend) InvalidateCommand(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.initialized {
		return nil
	}
	return b.invalidateCommandLocked(ctx)
}

func (b *TmuxBackend) initializeSessionLocked(ctx context.Context) error {
	if b.maxIdlePanes <= 0 {
		b.maxIdlePanes = defaultTmuxPanePoolSize
	}
	if b.initialized {
		return nil
	}

	args := []string{
		"new-session", "-d", "-P", "-F", "#{window_id} #{pane_id}",
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
		_ = b.killWindowLocked(ctx, rootPane.WindowID)
		return err
	}

	b.initialized = true
	b.activePane = nil
	b.idlePanes = []*tmuxPaneHandle{rootPane}
	return nil
}

func (b *TmuxBackend) closeSessionLocked(ctx context.Context) error {
	if !b.initialized {
		b.resetPoolStateLocked()
		return nil
	}
	if out, err := b.runTmuxLocked(ctx, "kill-session", "-t", b.session); err != nil {
		if isTmuxSessionMissing(out) {
			b.resetPoolStateLocked()
			return nil
		}
		return fmt.Errorf("failed to kill tmux session %q: %w", b.session, err)
	}
	b.resetPoolStateLocked()
	return nil
}

func (b *TmuxBackend) prepareCommandLocked(ctx context.Context) error {
	if b.activePane != nil {
		return fmt.Errorf("tmux command pane %q is still active", b.activePane.PaneID)
	}

	pane, err := b.acquirePaneLocked(ctx)
	if err != nil {
		return err
	}
	b.activePane = pane
	if err := b.clearPaneLocked(ctx, pane); err != nil {
		_ = b.killWindowLocked(ctx, pane.WindowID)
		b.activePane = nil
		return err
	}
	return nil
}

func (b *TmuxBackend) completeCommandLocked(ctx context.Context) error {
	if b.activePane == nil {
		return nil
	}
	pane := b.activePane
	b.activePane = nil

	if err := b.resetPaneForReuseLocked(ctx, pane); err != nil {
		_ = b.killWindowLocked(ctx, pane.WindowID)
		return err
	}
	if err := b.enqueueIdlePaneLocked(ctx, pane); err != nil {
		return err
	}
	return nil
}

func (b *TmuxBackend) invalidateCommandLocked(ctx context.Context) error {
	if b.activePane == nil {
		return nil
	}
	pane := b.activePane
	b.activePane = nil
	return b.killWindowLocked(ctx, pane.WindowID)
}

func (b *TmuxBackend) acquirePaneLocked(ctx context.Context) (*tmuxPaneHandle, error) {
	if len(b.idlePanes) > 0 {
		pane := b.idlePanes[0]
		b.idlePanes = b.idlePanes[1:]
		return pane, nil
	}
	return b.createPaneLocked(ctx)
}

func (b *TmuxBackend) createPaneLocked(ctx context.Context) (*tmuxPaneHandle, error) {
	args := []string{
		"new-window", "-d", "-P", "-F", "#{window_id} #{pane_id}",
		"-t", b.session,
	}
	if b.workingDir != "" {
		args = append(args, "-c", b.workingDir)
	}
	args = append(args, b.shellCommand())
	out, err := b.runTmuxLocked(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to create tmux pooled pane: %w", err)
	}

	pane, err := parseTmuxPaneHandle(out)
	if err != nil {
		return nil, fmt.Errorf("failed to parse tmux pooled pane: %w", err)
	}
	if err := b.bootstrapPaneLocked(ctx, pane); err != nil {
		_ = b.killWindowLocked(ctx, pane.WindowID)
		return nil, err
	}
	return pane, nil
}

func (b *TmuxBackend) enqueueIdlePaneLocked(ctx context.Context, pane *tmuxPaneHandle) error {
	b.idlePanes = append(b.idlePanes, pane)
	if len(b.idlePanes) <= b.maxIdlePanes {
		return nil
	}

	evicted := b.idlePanes[0]
	b.idlePanes = b.idlePanes[1:]
	if err := b.killWindowLocked(ctx, evicted.WindowID); err != nil {
		return fmt.Errorf("failed to evict tmux pooled pane %q: %w", evicted.PaneID, err)
	}
	return nil
}

func (b *TmuxBackend) resetPaneForReuseLocked(ctx context.Context, pane *tmuxPaneHandle) error {
	args := []string{"respawn-pane", "-k", "-t", pane.PaneID}
	if b.workingDir != "" {
		args = append(args, "-c", b.workingDir)
	}
	args = append(args, b.shellCommand())
	if _, err := b.runTmuxLocked(ctx, args...); err != nil {
		return fmt.Errorf("failed to respawn tmux pane %q for reuse: %w", pane.PaneID, err)
	}
	return b.bootstrapPaneLocked(ctx, pane)
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

func (b *TmuxBackend) sendKeysToCurrentPaneLocked(ctx context.Context, text string, enter bool) error {
	pane, err := b.requireCurrentPaneLocked()
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

func (b *TmuxBackend) readCurrentPaneLocked(ctx context.Context) (string, error) {
	pane, err := b.currentMetadataPaneLocked()
	if err != nil {
		return "", err
	}
	out, err := b.runTmuxLocked(ctx, "capture-pane", "-p", "-J", "-t", pane.target(), "-S", "-")
	if err != nil {
		return "", fmt.Errorf("failed to capture tmux pane %q: %w", pane.PaneID, err)
	}
	return out, nil
}

func (b *TmuxBackend) clearCurrentPaneLocked(ctx context.Context) error {
	pane, err := b.currentMetadataPaneLocked()
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

func (b *TmuxBackend) interruptCurrentPaneLocked(ctx context.Context) (bool, error) {
	pane, err := b.requireCurrentPaneLocked()
	if err != nil {
		return false, err
	}
	if _, err := b.runTmuxLocked(ctx, "send-keys", "-t", pane.target(), "C-c"); err != nil {
		return false, fmt.Errorf("failed to interrupt tmux pane %q: %w", pane.PaneID, err)
	}
	return true, nil
}

func (b *TmuxBackend) isCurrentPaneRunningLocked(ctx context.Context) (bool, error) {
	pane, err := b.requireCurrentPaneLocked()
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

func (b *TmuxBackend) currentPanePIDLocked(ctx context.Context) (*int, error) {
	pane, err := b.currentMetadataPaneLocked()
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

func (b *TmuxBackend) currentPaneWorkingDirLocked(ctx context.Context) (string, error) {
	pane, err := b.currentMetadataPaneLocked()
	if err != nil {
		return "", nil
	}
	out, err := b.runTmuxLocked(ctx, "display-message", "-p", "-t", pane.target(), "#{pane_current_path}")
	if err != nil {
		return "", fmt.Errorf("failed to inspect tmux working directory: %w", err)
	}
	return strings.TrimSpace(out), nil
}

func (b *TmuxBackend) currentMetadataPaneLocked() (*tmuxPaneHandle, error) {
	if b.activePane != nil {
		return b.activePane, nil
	}
	if len(b.idlePanes) > 0 {
		return b.idlePanes[0], nil
	}
	return nil, fmt.Errorf("tmux pane is not available")
}

func (b *TmuxBackend) requireCurrentPaneLocked() (*tmuxPaneHandle, error) {
	if b.activePane == nil {
		return nil, fmt.Errorf("tmux command pane is not active")
	}
	return b.activePane, nil
}

func (b *TmuxBackend) killWindowLocked(ctx context.Context, windowID string) error {
	if windowID == "" || !b.initialized {
		return nil
	}
	if out, err := b.runTmuxLocked(ctx, "kill-window", "-t", windowID); err != nil {
		if isTmuxSessionMissing(out) {
			return nil
		}
		return fmt.Errorf("failed to kill tmux window %q: %w", windowID, err)
	}
	return nil
}

func (b *TmuxBackend) resetPoolStateLocked() {
	b.initialized = false
	b.activePane = nil
	b.idlePanes = nil
	if b.maxIdlePanes <= 0 {
		b.maxIdlePanes = defaultTmuxPanePoolSize
	}
}

func (b *TmuxBackend) shellCommand() string {
	return b.shellPath + " --noprofile --norc -i"
}

func parseTmuxPaneHandle(output string) (*tmuxPaneHandle, error) {
	fields := strings.Fields(strings.TrimSpace(output))
	if len(fields) < 2 {
		return nil, fmt.Errorf("unexpected tmux pane output %q", strings.TrimSpace(output))
	}
	return &tmuxPaneHandle{
		WindowID: fields[0],
		PaneID:   fields[1],
	}, nil
}

func isTmuxSessionMissing(output string) bool {
	return strings.Contains(output, "can't find session") || strings.Contains(output, "no server running")
}
