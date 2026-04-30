package tool

import (
	"context"
	"fmt"
	"sync"

	sandboxpkg "github.com/wen/opentalon/internal/sandbox"
	terminalpkg "github.com/wen/opentalon/internal/tool/terminal"
	"github.com/wen/opentalon/pkg/logger"
)

type bashSessionExecutorEntry struct {
	route      bashRuntimeRoute
	workingDir string
	executor   *terminalpkg.Executor
	backend    terminalpkg.TerminalBackend
}

type bashSessionRegistry struct {
	mu       sync.Mutex
	sessions map[string]*bashSessionExecutorEntry
}

type bashSandboxInfoProvider interface {
	SandboxInfo() sandboxpkg.Info
}

func newBashSessionRegistry() *bashSessionRegistry {
	return &bashSessionRegistry{
		sessions: make(map[string]*bashSessionExecutorEntry),
	}
}

func (r *bashSessionRegistry) executorForSession(ctx context.Context, sessionID string, route bashRuntimeRoute, workingDir string) (*terminalpkg.Executor, error) {
	if route != bashRuntimeRouteSandbox {
		return newBashExecutorWithBackend(workingDir, newBashBackendForRoute(ctx, route, workingDir)), nil
	}
	if sessionID == "" {
		return nil, fmt.Errorf("bash 默认 sandbox 路径缺少 session.id，无法使用 executor")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if entry, ok := r.sessions[sessionID]; ok && entry != nil && entry.executor != nil {
		if entry.route != route {
			return nil, fmt.Errorf(
				"bash session %q 已绑定运行路径 %q，不能复用为 %q",
				sessionID,
				entry.route,
				route,
			)
		}
		if entry.workingDir != workingDir {
			return nil, fmt.Errorf(
				"bash session %q 已绑定工作目录 %q，不能复用为 %q",
				sessionID,
				entry.workingDir,
				workingDir,
			)
		}
		logger.InfoWithCtx(ctx, "bash 会话级 executor 已复用", r.auditArgsLocked(sessionID, entry)...)
		return entry.executor, nil
	}

	entry := &bashSessionExecutorEntry{
		route:      route,
		workingDir: workingDir,
	}
	backend := newManagedBashBackend(
		newBashBackendForRoute(ctx, route, workingDir),
		func(callbackCtx context.Context, cause error) {
			r.invalidateSession(callbackCtx, sessionID, entry, cause)
		},
	)
	entry.backend = backend
	entry.executor = newBashExecutorWithBackend(workingDir, backend)
	r.sessions[sessionID] = entry

	logger.InfoWithCtx(ctx, "bash 会话级 executor 已创建", r.auditArgsLocked(sessionID, entry)...)

	return entry.executor, nil
}

func (r *bashSessionRegistry) releaseSession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}

	entry := r.detachSession(sessionID)
	if entry == nil || entry.backend == nil {
		return nil
	}
	if err := entry.backend.Close(ctx); err != nil {
		return err
	}

	logger.InfoWithCtx(ctx, "bash 会话级 executor 已释放", r.auditArgsLocked(sessionID, entry)...)
	return nil
}

func (r *bashSessionRegistry) invalidateSession(ctx context.Context, sessionID string, expected *bashSessionExecutorEntry, cause error) {
	entry := r.detachExpectedSession(sessionID, expected)
	if entry == nil || entry.backend == nil {
		return
	}
	if err := entry.backend.Close(ctx); err != nil {
		args := append(r.auditArgsLocked(sessionID, entry), "error", err.Error())
		logger.WarnWithCtx(ctx, "bash 会话级 executor 失效清理失败", args...)
		return
	}

	args := append(r.auditArgsLocked(sessionID, entry), "cause", cause.Error())
	logger.WarnWithCtx(ctx, "bash 会话级 executor 已失效并清理", args...)
}

func (r *bashSessionRegistry) detachSession(sessionID string) *bashSessionExecutorEntry {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry := r.sessions[sessionID]
	delete(r.sessions, sessionID)
	return entry
}

func (r *bashSessionRegistry) detachExpectedSession(sessionID string, expected *bashSessionExecutorEntry) *bashSessionExecutorEntry {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry := r.sessions[sessionID]
	if entry == nil || entry != expected {
		return nil
	}
	delete(r.sessions, sessionID)
	return entry
}

func (r *bashSessionRegistry) auditArgsForSession(sessionID string) []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.auditArgsLocked(sessionID, r.sessions[sessionID])
}

func (r *bashSessionRegistry) auditArgsLocked(sessionID string, entry *bashSessionExecutorEntry) []any {
	args := []any{
		"session_id", sessionID,
	}
	if entry == nil {
		return args
	}
	args = append(args,
		"runtime_route", string(entry.route),
		"working_dir", entry.workingDir,
	)
	if infoProvider, ok := entry.backend.(bashSandboxInfoProvider); ok {
		info := infoProvider.SandboxInfo()
		if info.Status != "" {
			args = append(args, "sandbox_status", string(info.Status))
		}
		if info.Image != "" {
			args = append(args, "sandbox_image", info.Image)
		}
		if info.ContainerName != "" {
			args = append(args, "sandbox_container", info.ContainerName)
		}
	}
	return args
}

type managedBashBackend struct {
	inner               terminalpkg.TerminalBackend
	mu                  sync.Mutex
	initializeFailed    bool
	onInitializeFailure func(context.Context, error)
}

func newManagedBashBackend(inner terminalpkg.TerminalBackend, onInitializeFailure func(context.Context, error)) terminalpkg.TerminalBackend {
	return &managedBashBackend{
		inner:               inner,
		onInitializeFailure: onInitializeFailure,
	}
}

func (b *managedBashBackend) Initialize(ctx context.Context) error {
	if err := b.inner.Initialize(ctx); err != nil {
		b.mu.Lock()
		shouldInvalidate := !b.initializeFailed
		b.initializeFailed = true
		b.mu.Unlock()
		if shouldInvalidate && b.onInitializeFailure != nil {
			b.onInitializeFailure(ctx, err)
		}
		return err
	}
	return nil
}

func (b *managedBashBackend) Close(ctx context.Context) error {
	return b.inner.Close(ctx)
}

func (b *managedBashBackend) SendKeys(ctx context.Context, paneID, text string, enter bool) error {
	return b.inner.SendKeys(ctx, paneID, text, enter)
}

func (b *managedBashBackend) ReadScreen(ctx context.Context, paneID string) (string, error) {
	return b.inner.ReadScreen(ctx, paneID)
}

func (b *managedBashBackend) ClearScreen(ctx context.Context, paneID string) error {
	return b.inner.ClearScreen(ctx, paneID)
}

func (b *managedBashBackend) Interrupt(ctx context.Context, paneID string) (bool, error) {
	return b.inner.Interrupt(ctx, paneID)
}

func (b *managedBashBackend) IsRunning(ctx context.Context, paneID string) (bool, error) {
	return b.inner.IsRunning(ctx, paneID)
}

func (b *managedBashBackend) PrepareCommand(ctx context.Context, paneID string) error {
	lifecycle, ok := b.inner.(interface {
		PrepareCommand(context.Context, string) error
	})
	if !ok {
		return nil
	}
	return lifecycle.PrepareCommand(ctx, paneID)
}

func (b *managedBashBackend) CompleteCommand(ctx context.Context, paneID string) error {
	lifecycle, ok := b.inner.(interface {
		CompleteCommand(context.Context, string) error
	})
	if !ok {
		return nil
	}
	return lifecycle.CompleteCommand(ctx, paneID)
}

func (b *managedBashBackend) InvalidateCommand(ctx context.Context, paneID string) error {
	lifecycle, ok := b.inner.(interface {
		InvalidateCommand(context.Context, string) error
	})
	if !ok {
		return nil
	}
	return lifecycle.InvalidateCommand(ctx, paneID)
}

func (b *managedBashBackend) ResetPane(ctx context.Context, paneID string) error {
	lifecycle, ok := b.inner.(interface {
		ResetPane(context.Context, string) error
	})
	if !ok {
		return nil
	}
	return lifecycle.ResetPane(ctx, paneID)
}

func (b *managedBashBackend) PanePID(ctx context.Context, paneID string) (*int, error) {
	metadata, ok := b.inner.(interface {
		PanePID(context.Context, string) (*int, error)
	})
	if !ok {
		return nil, nil
	}
	return metadata.PanePID(ctx, paneID)
}

func (b *managedBashBackend) CurrentWorkingDir(ctx context.Context, paneID string) (string, error) {
	metadata, ok := b.inner.(interface {
		CurrentWorkingDir(context.Context, string) (string, error)
	})
	if !ok {
		return "", nil
	}
	return metadata.CurrentWorkingDir(ctx, paneID)
}
