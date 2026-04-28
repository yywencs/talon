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
	WorkingDir      string
	SkipMetadata    bool
	Err             error
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
func BashExecutor(ctx context.Context, action TerminalAction) *TerminalObservation {
	return defaultExecutor.Execute(ctx, action)
}

// Execute 执行 bash 工具请求并返回 observation。
func (e *Executor) Execute(ctx context.Context, action TerminalAction) *TerminalObservation {
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
		return errorOutput(action.Command, workingDir, nil, false, -1, err)
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
		return errorOutput(action.Command, workingDir, nil, false, -1, err)
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
	if result.Err != nil {
		result.Output = BuildTerminalErrorMessage(result.Err)
	}
	obsWorkingDir := workingDir
	if result.WorkingDir != "" {
		obsWorkingDir = result.WorkingDir
	}
	if !result.SkipMetadata {
		if result.PID == nil {
			result.PID = e.currentPID(execCtx)
		}
		obsWorkingDir = e.currentWorkingDir(execCtx, obsWorkingDir)
	}
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

func (e *Executor) executeWithBackend(ctx context.Context, action TerminalAction) commandResult {
	e.mu.Lock()
	defer e.mu.Unlock()

	if action.Reset {
		return e.resetWithBackend(ctx, action)
	}

	if err := e.backend.Initialize(ctx); err != nil {
		return commandResult{
			ExitCode: -1,
			Err: NewTerminalBackendOperationError(
				"初始化终端会话",
				err,
				"这通常表示当前运行环境中的终端后端不可用；如果持续失败，请停止自动重试并检查 tmux 或执行环境",
			),
		}
	}

	if action.IsInput {
		return e.executeInput(ctx, action.Command)
	}
	return e.executeCommand(ctx, action.Command)
}

func (e *Executor) resetWithBackend(ctx context.Context, action TerminalAction) commandResult {
	if err := e.backend.Close(ctx); err != nil {
		return commandResult{
			ExitCode: -1,
			Err: NewTerminalBackendOperationError(
				"重置终端会话",
				err,
				"可以先重试一次；如果 reset 持续失败，说明当前终端运行时可能已异常，需要人工检查或重建",
			),
		}
	}

	e.pending = nil

	if action.IsInput {
		if strings.TrimSpace(action.Command) == "" {
			return commandResult{
				Output:       "终端会话已重置",
				ExitCode:     0,
				WorkingDir:   e.workingDir,
				SkipMetadata: true,
			}
		}
		return commandResult{
			Output:       "终端会话已重置；发送输入前请先启动新命令",
			ExitCode:     -1,
			WorkingDir:   e.workingDir,
			SkipMetadata: true,
		}
	}

	if strings.TrimSpace(action.Command) == "" {
		return commandResult{
			Output:       "终端会话已重置",
			ExitCode:     0,
			WorkingDir:   e.workingDir,
			SkipMetadata: true,
		}
	}

	if err := e.backend.Initialize(ctx); err != nil {
		return commandResult{
			ExitCode: -1,
			Err: NewTerminalBackendOperationError(
				"在 reset 后初始化终端会话",
				err,
				"可以先重试一次；如果仍然无法创建新的终端会话，请检查 tmux 或执行环境",
			),
		}
	}
	return e.executeCommand(ctx, action.Command)
}

func (e *Executor) executeCommand(ctx context.Context, command string) commandResult {
	if e.pending != nil {
		running, err := e.syncPendingState(ctx)
		if err != nil {
			return commandResult{
				ExitCode: -1,
				PID:      e.currentPID(ctx),
				Err:      err,
			}
		}
		if running {
			return commandResult{
				ExitCode: -1,
				PID:      e.currentPID(ctx),
				Err: NewTerminalStateError(
					"当前仍有命令在运行，请使用 is_input=true 继续读取输出或发送输入",
					"暂时不要启动新命令；请改用 is_input=true 继续交互、发送后续输入，或使用 C-c 中断当前命令",
				),
			}
		}
	}

	lifecycle := e.backendLifecycle()
	if lifecycle != nil {
		if err := lifecycle.PrepareCommand(ctx); err != nil {
			return commandResult{
				ExitCode: -1,
				Err: NewTerminalBackendOperationError(
					"分配 tmux 命令 pane",
					err,
					"可以先重试一次；如果持续分配失败，请使用 reset=true 重建共享会话后再执行命令",
				),
			}
		}
	}

	screen, err := e.backend.ReadScreen(ctx)
	if err != nil {
		e.invalidateBackendCommand(ctx)
		return commandResult{
			ExitCode: -1,
			PID:      e.currentPID(ctx),
			Err: NewTerminalBackendOperationError(
				"在执行命令前读取终端屏幕",
				err,
				"可以先重试一次；如果仍然无法稳定读取终端屏幕，请使用 reset=true 重建当前会话",
			),
		}
	}

	marker := newExecutionMarker()
	if err := e.backend.SendKeys(ctx, wrapCommandForSession(command, marker), true); err != nil {
		e.invalidateBackendCommand(ctx)
		return commandResult{
			ExitCode: -1,
			PID:      e.currentPID(ctx),
			Err: NewTerminalBackendOperationError(
				"向终端会话发送命令",
				err,
				"请检查命令中是否包含不受支持的控制输入；如果会话状态异常，请使用 reset=true 后重新执行命令",
			),
		}
	}

	e.pending = &pendingExecution{
		Marker:     marker,
		LastScreen: screen,
	}
	return e.collectPendingResult(ctx)
}

func (e *Executor) executeInput(ctx context.Context, command string) commandResult {
	trimmed := strings.TrimSpace(command)
	if e.pending != nil && trimmed != "" {
		running, err := e.syncPendingState(ctx)
		if err != nil {
			return commandResult{
				ExitCode: -1,
				PID:      e.currentPID(ctx),
				Err:      err,
			}
		}
		if !running {
			return commandResult{
				ExitCode: -1,
				PID:      e.currentPID(ctx),
				Err: NewTerminalStateError(
					"is_input 需要当前存在可继续交互的运行中命令",
					"请先启动一个命令，并在它超时或进入交互态后，再通过 is_input=true 继续发送输入",
				),
			}
		}
	}
	if e.pending == nil {
		if trimmed == "" {
			return commandResult{
				Output:   "",
				ExitCode: 0,
				PID:      e.currentPID(ctx),
			}
		}
		return commandResult{
			ExitCode: -1,
			PID:      e.currentPID(ctx),
			Err: NewTerminalStateError(
				"is_input 需要当前存在可继续交互的运行中命令",
				"请先启动一个命令，并在它超时或进入交互态后，再通过 is_input=true 继续发送输入",
			),
		}
	}

	if trimmed != "" {
		if isInterruptCommand(trimmed) {
			interrupted, err := e.backend.Interrupt(ctx)
			if err != nil {
				return commandResult{
					ExitCode: -1,
					PID:      e.currentPID(ctx),
					Err: NewTerminalBackendOperationError(
						"中断终端会话",
						err,
						"可以先重试一次中断；如果仍然失败，请使用 reset=true 终止当前会话并重新开始",
					),
				}
			}
			if !interrupted {
				return commandResult{
					ExitCode: -1,
					PID:      e.currentPID(ctx),
					Err: NewTerminalStateError(
						"终端会话未报告可中断的前台目标",
						"前台进程可能已经退出；请先读取剩余输出，或启动新命令，而不是盲目重复发送同一输入",
					),
				}
			}
		} else {
			if err := e.backend.SendKeys(ctx, command, true); err != nil {
				return commandResult{
					ExitCode: -1,
					PID:      e.currentPID(ctx),
					Err: NewTerminalBackendOperationError(
						"向终端会话发送输入",
						err,
						"可以先重试一次输入；如果持续发送失败，请使用 reset=true 并从头重新启动命令链路",
					),
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
			e.pending = nil
			e.invalidateBackendCommand(ctx)
			return commandResult{
				ExitCode: -1,
				PID:      e.currentPID(ctx),
				Err: NewTerminalBackendOperationError(
					"读取终端屏幕",
					err,
					"可以先重试一次，或继续用 is_input=true 拉取输出；如果仍然持续读取失败，请使用 reset=true 重建会话",
				),
			}
		}
		delta := diffScreen(e.pending.LastScreen, screen)
		output, exitCode, completed, parseErr := parseCompletedDelta(delta, e.pending.Marker)
		if parseErr != nil {
			e.pending = nil
			e.invalidateBackendCommand(ctx)
			return commandResult{
				ExitCode: -1,
				PID:      e.currentPID(ctx),
				Err: NewTerminalExecutionError(
					fmt.Sprintf("解析终端命令结果失败: %v", parseErr),
					parseErr,
					"终端输出格式已经变得不一致；请使用 reset=true 后重新执行命令，或在命令会输出控制字符时尽量简化输出",
				),
			}
		}
		if completed {
			pid, workingDir := e.captureCurrentMetadata(ctx, "")
			e.pending = nil
			if err := e.completeBackendCommand(ctx); err != nil {
				return commandResult{
					ExitCode:   -1,
					PID:        pid,
					WorkingDir: workingDir,
					Err: NewTerminalBackendOperationError(
						"回收 tmux 命令 pane",
						err,
						"共享 session 的 pane 回收失败；请先重试一次，必要时使用 reset=true 重建会话以恢复池状态",
					),
				}
			}
			return commandResult{
				Output:     output,
				ExitCode:   exitCode,
				PID:        pid,
				WorkingDir: workingDir,
			}
		}

		if ctx.Err() != nil {
			e.pending.LastScreen = screen
			msg := "上下文已取消"
			timedOut := false
			if ctx.Err() == context.DeadlineExceeded {
				msg = "命令执行超时"
				timedOut = true
			}
			return commandResult{
				ExitCode: -1,
				TimedOut: timedOut,
				PID:      e.currentPID(ctx),
				Err: NewTerminalExecutionError(
					appendProcessMessage(delta, msg),
					ctx.Err(),
					"如果该进程本来就应该持续运行，请使用 is_input=true 继续读取输出或发送输入；否则请用 C-c 中断，或使用更长的超时时间重新执行",
				),
			}
		}

		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

func (e *Executor) syncPendingState(ctx context.Context) (bool, error) {
	if e.pending == nil {
		return false, nil
	}

	running, err := e.backend.IsRunning(ctx)
	if err != nil {
		e.pending = nil
		return false, NewTerminalBackendOperationError(
			"检查终端会话运行状态",
			err,
			"当前会话状态已经不再可信；请先使用 reset=true 重建终端会话后再重试",
		)
	}
	if !running {
		e.pending = nil
		if err := e.completeBackendCommand(ctx); err != nil {
			return false, NewTerminalBackendOperationError(
				"清理已结束命令对应的 tmux pane",
				err,
				"共享 pane 清理失败；请先重试一次，如果仍然失败请使用 reset=true 重建会话",
			)
		}
		return false, NewTerminalStateError(
			"由于终端中已没有运行中的前台命令，pending 执行状态已被清理；请使用新命令重试",
			"之前的交互命令已经退出，因此它的 pending 状态已被丢弃；请重新执行命令以建立新的交互链路",
		)
	}
	return true, nil
}

func (e *Executor) backendLifecycle() terminalBackendCommandLifecycle {
	lifecycle, ok := e.backend.(terminalBackendCommandLifecycle)
	if !ok {
		return nil
	}
	return lifecycle
}

func (e *Executor) completeBackendCommand(ctx context.Context) error {
	lifecycle := e.backendLifecycle()
	if lifecycle == nil {
		return nil
	}
	return lifecycle.CompleteCommand(ctx)
}

func (e *Executor) invalidateBackendCommand(ctx context.Context) {
	lifecycle := e.backendLifecycle()
	if lifecycle == nil {
		return
	}
	_ = lifecycle.InvalidateCommand(ctx)
}

func (e *Executor) captureCurrentMetadata(ctx context.Context, fallbackWorkingDir string) (*int, string) {
	pid := e.currentPID(ctx)
	workingDir := e.currentWorkingDir(ctx, fallbackWorkingDir)
	return pid, workingDir
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
	return output[:maxOutputSize] + "[输出已截断]", true
}
