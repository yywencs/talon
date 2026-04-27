package terminal

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wen/opentalon/pkg/logger"
)

const commandExitPrefix = "__OPENTALON_EXIT__:"

type commandResult struct {
	Output          string
	ExitCode        int
	TimedOut        bool
	PID             *int
	OutputTruncated bool
}

type pendingExecution struct {
	Marker     string
	LastScreen string
}

// ExecutorConfig 定义终端执行器的初始化参数。
type ExecutorConfig struct {
	// WorkingDir 表示执行器默认工作目录。
	WorkingDir string
	// DefaultTimeout 表示默认超时时间。
	DefaultTimeout time.Duration
	// Backend 表示执行器使用的终端会话后端。
	Backend TerminalBackend
}

// Executor 表示终端工具在当前运行环境中的执行器。
type Executor struct {
	workingDir     string
	defaultTimeout time.Duration
	backend        TerminalBackend
	mu             sync.Mutex
	pending        *pendingExecution
}

var defaultExecutor = NewExecutor(ExecutorConfig{})

// NewExecutor 创建终端执行器。
func NewExecutor(config ExecutorConfig) *Executor {
	defaultTimeout := time.Duration(defaultTimeoutSecs) * time.Second
	if config.DefaultTimeout > 0 {
		defaultTimeout = config.DefaultTimeout
	}
	backend := config.Backend
	if backend == nil {
		backend = NewTmuxBackend(config.WorkingDir)
	}
	return &Executor{
		workingDir:     config.WorkingDir,
		defaultTimeout: defaultTimeout,
		backend:        backend,
	}
}

// BashExecutor 使用默认执行器执行 bash 工具请求并返回 observation。
func BashExecutor(ctx context.Context, action BashTool) *TerminalObservation {
	return defaultExecutor.Execute(ctx, action)
}

// Execute 执行 bash 工具请求并返回 observation。
func (e *Executor) Execute(ctx context.Context, action BashTool) *TerminalObservation {
	workingDir := e.workingDir

	if err := validateAction(&action); err != nil {
		logger.WarnWithCtx(ctx, "审计: bash 命令校验失败",
			"tool_name", "bash",
			"command_name", auditCommandName(action.Command),
			"command_sha256", auditCommandHash(action.Command),
			"working_dir", workingDir,
			"security_risk", string(action.SecurityRisk),
			"error", err.Error(),
		)
		return errorOutput(action.Command, workingDir, nil, false, -1, err.Error())
	}

	if err := validateWorkingDir(workingDir); err != nil {
		logger.WarnWithCtx(ctx, "审计: bash 工作目录校验失败",
			"tool_name", "bash",
			"command_name", auditCommandName(action.Command),
			"command_sha256", auditCommandHash(action.Command),
			"working_dir", workingDir,
			"security_risk", string(action.SecurityRisk),
			"error", err.Error(),
		)
		return errorOutput(action.Command, workingDir, nil, false, -1, err.Error())
	}

	timeout := e.resolveTimeout(action.Timeout)

	logger.DebugWithCtx(ctx, "审计: bash 命令开始执行",
		"tool_name", "bash",
		"command_name", auditCommandName(action.Command),
		"command_sha256", auditCommandHash(action.Command),
		"working_dir", workingDir,
		"timeout_secs", timeout,
		"security_risk", string(action.SecurityRisk),
	)

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout*float64(time.Second)))
	defer cancel()

	result := e.executeWithBackend(execCtx, action)
	if result.PID == nil {
		result.PID = e.currentPID(execCtx)
	}
	obsWorkingDir := e.currentWorkingDir(execCtx, workingDir)
	result.Output, result.OutputTruncated = truncateIfNeeded(result.Output)
	logTerminalCommandCompletion(ctx, action, workingDir, timeout, result)
	return NewTerminalObservation(action.Command, obsWorkingDir, result.PID, result.TimedOut, result.ExitCode, result.Output)
}

func (e *Executor) resolveTimeout(timeout *float64) float64 {
	if timeout != nil && *timeout > 0 {
		return *timeout
	}
	return e.defaultTimeout.Seconds()
}

func (e *Executor) executeWithBackend(ctx context.Context, action BashTool) commandResult {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.backend.Initialize(ctx); err != nil {
		return commandResult{
			Output:   err.Error(),
			ExitCode: -1,
		}
	}

	if action.Reset {
		return commandResult{
			Output:   "reset is not implemented yet",
			ExitCode: -1,
			PID:      e.currentPID(ctx),
		}
	}
	if action.IsInput {
		return e.executeInput(ctx, action.Command)
	}
	return e.executeCommand(ctx, action.Command)
}

func (e *Executor) executeCommand(ctx context.Context, command string) commandResult {
	if e.pending != nil {
		return commandResult{
			Output:   "a command is still running; use is_input=true to continue reading output or send input",
			ExitCode: -1,
			PID:      e.currentPID(ctx),
		}
	}

	screen, err := e.backend.ReadScreen(ctx)
	if err != nil {
		return commandResult{
			Output:   fmt.Sprintf("failed to read terminal screen before command: %v", err),
			ExitCode: -1,
			PID:      e.currentPID(ctx),
		}
	}

	marker := newExecutionMarker()
	if err := e.backend.SendKeys(ctx, wrapCommandForSession(command, marker), true); err != nil {
		return commandResult{
			Output:   fmt.Sprintf("failed to send command to terminal session: %v", err),
			ExitCode: -1,
			PID:      e.currentPID(ctx),
		}
	}

	e.pending = &pendingExecution{
		Marker:     marker,
		LastScreen: screen,
	}
	return e.collectPendingResult(ctx)
}

func (e *Executor) executeInput(ctx context.Context, command string) commandResult {
	if e.pending == nil {
		if strings.TrimSpace(command) == "" {
			return commandResult{
				Output:   "",
				ExitCode: 0,
				PID:      e.currentPID(ctx),
			}
		}
		return commandResult{
			Output:   "is_input requires a running command",
			ExitCode: -1,
			PID:      e.currentPID(ctx),
		}
	}

	trimmed := strings.TrimSpace(command)
	if trimmed != "" {
		if isInterruptCommand(trimmed) {
			interrupted, err := e.backend.Interrupt(ctx)
			if err != nil {
				return commandResult{
					Output:   fmt.Sprintf("failed to interrupt terminal session: %v", err),
					ExitCode: -1,
					PID:      e.currentPID(ctx),
				}
			}
			if !interrupted {
				return commandResult{
					Output:   "terminal session did not report an interrupt target",
					ExitCode: -1,
					PID:      e.currentPID(ctx),
				}
			}
		} else {
			if err := e.backend.SendKeys(ctx, command, true); err != nil {
				return commandResult{
					Output:   fmt.Sprintf("failed to send input to terminal session: %v", err),
					ExitCode: -1,
					PID:      e.currentPID(ctx),
				}
			}
		}
	}

	return e.collectPendingResult(ctx)
}

func (e *Executor) collectPendingResult(ctx context.Context) commandResult {
	if e.pending == nil {
		return commandResult{
			Output:   "",
			ExitCode: 0,
			PID:      e.currentPID(ctx),
		}
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		screen, err := e.backend.ReadScreen(ctx)
		if err != nil {
			return commandResult{
				Output:   fmt.Sprintf("failed to read terminal screen: %v", err),
				ExitCode: -1,
				PID:      e.currentPID(ctx),
			}
		}
		delta := diffScreen(e.pending.LastScreen, screen)
		output, exitCode, completed, parseErr := parseCompletedDelta(delta, e.pending.Marker)
		if parseErr != nil {
			return commandResult{
				Output:   fmt.Sprintf("failed to parse terminal command result: %v", parseErr),
				ExitCode: -1,
				PID:      e.currentPID(ctx),
			}
		}
		if completed {
			e.pending = nil
			return commandResult{
				Output:   output,
				ExitCode: exitCode,
				PID:      e.currentPID(ctx),
			}
		}

		if ctx.Err() != nil {
			e.pending.LastScreen = screen
			msg := "context cancelled"
			timedOut := false
			if ctx.Err() == context.DeadlineExceeded {
				msg = "command timed out"
				timedOut = true
			}
			return commandResult{
				Output:   appendProcessMessage(delta, msg),
				ExitCode: -1,
				TimedOut: timedOut,
				PID:      e.currentPID(ctx),
			}
		}

		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

func (e *Executor) currentPID(ctx context.Context) *int {
	metadataBackend, ok := e.backend.(terminalBackendMetadata)
	if !ok {
		return nil
	}
	pid, err := metadataBackend.PanePID(ctx)
	if err != nil {
		return nil
	}
	return pid
}

func (e *Executor) currentWorkingDir(ctx context.Context, fallback string) string {
	metadataBackend, ok := e.backend.(terminalBackendMetadata)
	if !ok {
		return fallback
	}
	workingDir, err := metadataBackend.CurrentWorkingDir(ctx)
	if err != nil || workingDir == "" {
		return fallback
	}
	return workingDir
}

func newExecutionMarker() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")
}

func wrapCommandForSession(command, marker string) string {
	return fmt.Sprintf(
		"%s; __opentalon_exit_code=$?; printf '%s%s:%%s\\n' \"$__opentalon_exit_code\"",
		command,
		commandExitPrefix,
		marker,
	)
}

func parseCompletedDelta(delta, marker string) (string, int, bool, error) {
	markerToken := commandExitPrefix + marker + ":"
	before, rest, found := strings.Cut(delta, markerToken)
	if !found {
		return "", 0, false, nil
	}

	exitCodeText, _, hasNewline := strings.Cut(rest, "\n")
	if !hasNewline {
		return "", 0, false, nil
	}

	exitCode, err := strconv.Atoi(strings.TrimSpace(exitCodeText))
	if err != nil {
		return "", 0, false, err
	}
	return before, exitCode, true, nil
}

func diffScreen(previous, current string) string {
	if previous == "" {
		return current
	}
	if strings.HasPrefix(current, previous) {
		return current[len(previous):]
	}
	return current
}

func isInterruptCommand(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "^c", "c-c", "ctrl+c":
		return true
	default:
		return false
	}
}

func appendProcessMessage(output, msg string) string {
	if output == "" {
		return msg
	}
	if strings.HasSuffix(output, "\n") {
		return output + msg
	}
	return output + "\n" + msg
}

func truncateIfNeeded(output string) (string, bool) {
	if len(output) <= maxOutputSize {
		return output, false
	}
	return output[:maxOutputSize] + "[output truncated]", true
}
