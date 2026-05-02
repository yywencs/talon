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
	runtimeRoute        string
	workingDir          string
	executor            *terminalpkg.Executor
	backend             terminalpkg.TerminalBackend
	sandboxInfoProvider func() sandboxpkg.Info
}

type bashSessionRegistry struct {
	mu       sync.Mutex
	sessions map[string]*bashSessionExecutorEntry
}

func newBashSessionRegistry() *bashSessionRegistry {
	return &bashSessionRegistry{
		sessions: make(map[string]*bashSessionExecutorEntry),
	}
}

func (r *bashSessionRegistry) executorForSession(
	ctx context.Context,
	sessionID string,
	runtimeRoute string,
	workingDir string,
	createBundle func(context.Context, string) bashBackendBundle,
) (*terminalpkg.Executor, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("bash 默认 sandbox 路径缺少 session.id，无法使用 executor")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if entry, ok := r.sessions[sessionID]; ok && entry != nil && entry.executor != nil {
		if entry.runtimeRoute != runtimeRoute {
			return nil, fmt.Errorf(
				"bash session %q 已绑定运行路径 %q，不能复用为 %q",
				sessionID,
				entry.runtimeRoute,
				runtimeRoute,
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
		runtimeRoute: runtimeRoute,
		workingDir:   workingDir,
	}
	bundle := createBundle(ctx, workingDir)
	entry.backend = bundle.backend
	entry.sandboxInfoProvider = bundle.sandboxInfoProvider
	entry.executor = newBashExecutorWithBackend(workingDir, bundle.backend, func(callbackCtx context.Context, cause error) {
		r.invalidateSession(callbackCtx, sessionID, entry, cause)
	})
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

func (r *bashSessionRegistry) pathMapperForSession(sessionID string) (PathMapper, bool) {
	if sessionID == "" {
		return PathMapper{}, false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	entry := r.sessions[sessionID]
	if entry == nil {
		return PathMapper{}, false
	}
	return r.pathMapperLocked(entry)
}

func (r *bashSessionRegistry) auditArgsLocked(sessionID string, entry *bashSessionExecutorEntry) []any {
	args := []any{
		"session_id", sessionID,
	}
	if entry == nil {
		return args
	}
	args = append(args,
		"runtime_route", entry.runtimeRoute,
		"working_dir", entry.workingDir,
	)
	if entry.sandboxInfoProvider != nil {
		info := entry.sandboxInfoProvider()
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

func (r *bashSessionRegistry) pathMapperLocked(entry *bashSessionExecutorEntry) (PathMapper, bool) {
	if entry == nil {
		return PathMapper{}, false
	}

	hostRoot := entry.workingDir
	runtimeRoot := entry.workingDir
	if entry.sandboxInfoProvider != nil {
		info := entry.sandboxInfoProvider()
		if info.HostWorkingDir != "" {
			hostRoot = info.HostWorkingDir
		}
		if info.ContainerWorkDir != "" {
			runtimeRoot = info.ContainerWorkDir
		}
	}
	if hostRoot == "" || runtimeRoot == "" {
		return PathMapper{}, false
	}
	return NewPathMapper(hostRoot, runtimeRoot), true
}
