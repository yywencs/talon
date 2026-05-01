package tool

import (
	"context"
	"fmt"
	"os"
	"strings"

	sandboxpkg "github.com/wen/opentalon/internal/sandbox"
	terminalpkg "github.com/wen/opentalon/internal/tool/terminal"
	"github.com/wen/opentalon/pkg/logger"
	"github.com/wen/opentalon/pkg/utils"
)

type CmdOutputMetadata = terminalpkg.CmdOutputMetadata

type TerminalAction = terminalpkg.TerminalAction
type TerminalObservation = terminalpkg.TerminalObservation

const (
	bashRuntimeLabelSandbox = "sandbox"
	bashRuntimeLabelHost    = "host"
)

type sessionIDContextKey struct{}

type bashBackendBundle struct {
	backend             terminalpkg.TerminalBackend
	sandboxInfoProvider func() sandboxpkg.Info
}

type bashBackendBundleFactory func(context.Context, string) bashBackendBundle

var resolveBashWorkingDir = func() string {
	if root, err := utils.FindWorkspaceRoot(); err == nil && root != "" {
		return root
	}
	wd, err := os.Getwd()
	if err == nil {
		return wd
	}
	return ""
}

var newHostBashBackendBundle bashBackendBundleFactory = func(ctx context.Context, workingDir string) bashBackendBundle {
	_ = ctx
	return bashBackendBundle{
		backend: sandboxpkg.NewHostTmuxBackend(workingDir),
	}
}

var newSandboxBashBackendBundle bashBackendBundleFactory = func(ctx context.Context, workingDir string) bashBackendBundle {
	_ = ctx
	sb := sandboxpkg.NewDockerSandbox(sandboxpkg.Config{WorkingDir: workingDir})
	return bashBackendBundle{
		backend:             sandboxpkg.NewSandboxTmuxBackend(workingDir, sb),
		sandboxInfoProvider: sb.Info,
	}
}

var bashSessionExecutors = newBashSessionRegistry()

func newBashExecutorWithBackend(workingDir string, backend terminalpkg.TerminalBackend, onBackendFailure func(context.Context, error)) *terminalpkg.Executor {
	return terminalpkg.NewExecutor(terminalpkg.ExecutorConfig{
		WorkingDir:       workingDir,
		Backend:          backend,
		OnBackendFailure: onBackendFailure,
	})
}

func resolveReusableBashExecutor(
	ctx context.Context,
	runtimeRoute string,
	workingDir string,
	createBundle bashBackendBundleFactory,
) (*terminalpkg.Executor, error) {
	sessionID := SessionIDFromContext(ctx)
	return bashSessionExecutors.executorForSession(ctx, sessionID, runtimeRoute, workingDir, createBundle)
}

// ReleaseBashSession 释放指定 session.id 绑定的 bash 会话级 executor 及其后端资源。
func ReleaseBashSession(ctx context.Context, sessionID string) error {
	if err := bashSessionExecutors.releaseSession(ctx, sessionID); err != nil {
		return fmt.Errorf("release bash session %q: %w", sessionID, err)
	}
	return nil
}

func resolveEphemeralBashExecutor(
	ctx context.Context,
	workingDir string,
	createBundle bashBackendBundleFactory,
) (*terminalpkg.Executor, error) {
	bundle := createBundle(ctx, workingDir)
	return newBashExecutorWithBackend(workingDir, bundle.backend, nil), nil
}

func bashExecutor(
	workingDir string,
	resolveExecutor func(context.Context, string) (*terminalpkg.Executor, error),
) func(ctx context.Context, action TerminalAction) *TerminalObservation {
	return func(ctx context.Context, action TerminalAction) *TerminalObservation {
		if strings.TrimSpace(action.PaneID) == "" {
			action.PaneID = "default_main"
		}
		executor, err := resolveExecutor(ctx, workingDir)
		if err != nil {
			return terminalpkg.NewTerminalErrorObservation(action.Command, workingDir, action.PaneID, nil, false, -1, err)
		}
		logBashToolCall(ctx, "bash 调用开始", action)
		observation := executor.Execute(ctx, action)
		logBashToolResult(ctx, action, observation)
		return observation
	}
}

func logBashToolCall(ctx context.Context, message string, action TerminalAction) {
	args := append(
		bashSessionExecutors.auditArgsForSession(SessionIDFromContext(ctx)),
		"pane_id", action.PaneID,
		"is_input", action.IsInput,
		"reset", action.Reset,
	)
	logger.InfoWithCtx(ctx, message, args...)
}

func logBashToolResult(ctx context.Context, action TerminalAction, observation *TerminalObservation) {
	args := append(
		bashSessionExecutors.auditArgsForSession(SessionIDFromContext(ctx)),
		"pane_id", action.PaneID,
	)
	if observation != nil {
		args = append(
			args,
			"exit_code", observation.ExitCodeValue(),
			"timeout", observation.Timeout,
		)
		if observation.Metadata.WorkingDir != "" {
			args = append(args, "observation_working_dir", observation.Metadata.WorkingDir)
		}
	}
	logger.InfoWithCtx(ctx, "bash 调用结束", args...)
}

const TOOL_DESCRIPTION = terminalpkg.ToolDescription

func newBashTool() *BaseTool[TerminalAction, *TerminalObservation] {
	workingDir := resolveBashWorkingDir()
	return &BaseTool[TerminalAction, *TerminalObservation]{
		ToolName: "bash",
		ToolDesc: TOOL_DESCRIPTION,
		Executor: bashExecutor(workingDir, func(ctx context.Context, wd string) (*terminalpkg.Executor, error) {
			return resolveReusableBashExecutor(ctx, bashRuntimeLabelSandbox, wd, newSandboxBashBackendBundle)
		}),
	}
}

func newHostBashTool() *BaseTool[TerminalAction, *TerminalObservation] {
	workingDir := resolveBashWorkingDir()
	return &BaseTool[TerminalAction, *TerminalObservation]{
		ToolName: "bash",
		ToolDesc: TOOL_DESCRIPTION,
		Executor: bashExecutor(workingDir, func(ctx context.Context, wd string) (*terminalpkg.Executor, error) {
			return resolveEphemeralBashExecutor(ctx, wd, newHostBashBackendBundle)
		}),
	}
}

func init() {
	Register("bash", func(ctx context.Context) Tool {
		_ = ctx
		return newBashTool()
	})
}

// ContextWithSessionID 将会话 ID 注入工具执行上下文。
func ContextWithSessionID(ctx context.Context, sessionID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sessionIDContextKey{}, sessionID)
}

// SessionIDFromContext 从工具执行上下文中提取会话 ID。
func SessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	sessionID, _ := ctx.Value(sessionIDContextKey{}).(string)
	return sessionID
}
