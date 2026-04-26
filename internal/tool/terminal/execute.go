package terminal

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/wen/opentalon/pkg/logger"
)

type commandResult struct {
	Output          string
	ExitCode        int
	TimedOut        bool
	PID             *int
	OutputTruncated bool
}

// ExecutorConfig 定义终端执行器的初始化参数。
type ExecutorConfig struct {
	// WorkingDir 表示执行器默认工作目录。
	WorkingDir string
	// DefaultTimeout 表示默认超时时间。
	DefaultTimeout time.Duration
}

// Executor 表示终端工具在当前运行环境中的执行器。
type Executor struct {
	workingDir     string
	defaultTimeout time.Duration
}

var defaultExecutor = NewExecutor(ExecutorConfig{})

// NewExecutor 创建终端执行器。
func NewExecutor(config ExecutorConfig) *Executor {
	defaultTimeout := time.Duration(defaultTimeoutSecs) * time.Second
	if config.DefaultTimeout > 0 {
		defaultTimeout = config.DefaultTimeout
	}
	return &Executor{
		workingDir:     config.WorkingDir,
		defaultTimeout: defaultTimeout,
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

	if action.Reset {
		return errorOutput(action.Command, workingDir, nil, false, -1, "reset is not implemented yet")
	}
	if action.IsInput {
		return errorOutput(action.Command, workingDir, nil, false, -1, "is_input is not implemented yet")
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

	result := runBash(execCtx, action.Command, workingDir)
	result.Output, result.OutputTruncated = truncateIfNeeded(result.Output)
	logTerminalCommandCompletion(ctx, action, workingDir, timeout, result)
	return NewTerminalObservation(action.Command, workingDir, result.PID, result.TimedOut, result.ExitCode, result.Output)
}

func (e *Executor) resolveTimeout(timeout *float64) float64 {
	if timeout != nil && *timeout > 0 {
		return *timeout
	}
	return e.defaultTimeout.Seconds()
}

func runBash(ctx context.Context, command, workingDir string) commandResult {
	cmd := exec.Command("bash", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if workingDir != "" {
		cmd.Dir = workingDir
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		logger.ErrorWithCtx(ctx, "bash 子进程创建 stdout 管道失败",
			"command_name", auditCommandName(command),
			"command_sha256", auditCommandHash(command),
			"working_dir", workingDir,
			"error", err.Error(),
		)
		return commandResult{Output: fmt.Sprintf("failed to create stdout pipe: %v", err), ExitCode: -1}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		logger.ErrorWithCtx(ctx, "bash 子进程创建 stderr 管道失败",
			"command_name", auditCommandName(command),
			"command_sha256", auditCommandHash(command),
			"working_dir", workingDir,
			"error", err.Error(),
		)
		return commandResult{Output: fmt.Sprintf("failed to create stderr pipe: %v", err), ExitCode: -1}
	}

	if err := cmd.Start(); err != nil {
		logger.ErrorWithCtx(ctx, "bash 子进程启动失败",
			"command_name", auditCommandName(command),
			"command_sha256", auditCommandHash(command),
			"working_dir", workingDir,
			"error", err.Error(),
		)
		return commandResult{Output: fmt.Sprintf("failed to start: %v", err), ExitCode: -1}
	}

	pid := cmd.Process.Pid

	var (
		outBuilder strings.Builder
		writeMu    sync.Mutex
		copyWG     sync.WaitGroup
	)

	writeChunk := func(p []byte) (int, error) {
		writeMu.Lock()
		defer writeMu.Unlock()
		return outBuilder.Write(p)
	}

	copyWG.Add(2)
	go func() {
		defer copyWG.Done()
		_, _ = io.Copy(writerFunc(writeChunk), stdout)
	}()
	go func() {
		defer copyWG.Done()
		_, _ = io.Copy(writerFunc(writeChunk), stderr)
	}()

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		copyWG.Wait()
		exitCode := 0
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		if err != nil && exitCode == 0 {
			exitCode = -1
		}
		return commandResult{
			Output:   outBuilder.String(),
			ExitCode: exitCode,
			PID:      intPtr(pid),
		}
	case <-ctx.Done():
		if cmd.Process != nil {
			if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
				logger.ErrorWithCtx(ctx, "bash 子进程终止失败",
					"command_name", auditCommandName(command),
					"command_sha256", auditCommandHash(command),
					"pid", cmd.Process.Pid,
					"error", err.Error(),
				)
			}
		}
		<-done
		copyWG.Wait()

		output := outBuilder.String()
		if ctx.Err() == context.DeadlineExceeded {
			return commandResult{
				Output:   appendProcessMessage(output, "command timed out"),
				ExitCode: -1,
				TimedOut: true,
				PID:      intPtr(pid),
			}
		}
		return commandResult{
			Output:   appendProcessMessage(output, "context cancelled"),
			ExitCode: -1,
			PID:      intPtr(pid),
		}
	}
}

type writerFunc func(p []byte) (int, error)

func (fn writerFunc) Write(p []byte) (int, error) {
	return fn(p)
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
