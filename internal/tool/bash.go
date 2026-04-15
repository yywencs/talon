package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/wen/opentalon/internal/types"
)

const (
	defaultTimeoutSecs = 30
	maxTimeoutSecs     = 300
	maxOutputSize      = 1024 * 1024
)

type BashTool struct{}

func NewBashTool() *BashTool {
	return &BashTool{}
}

func (t *BashTool) Name() string {
	return "bash"
}

func (t *BashTool) Description() string {
	return "在宿主机的 shell 环境中执行一条 Bash 命令。命令将在 `os.Exec` 中运行，支持管道、重定向等 bash 特性。返回命令的 stdout/stderr 混合输出及退出码。"
}

func (t *BashTool) Execute(ctx context.Context, rawArgs []byte) Observation {
	var action BashAction
	if err := json.Unmarshal(rawArgs, &action); err != nil {
		return errorOutput(-1, "invalid JSON arguments: "+err.Error())
	}

	if err := validateAction(&action); err != nil {
		return errorOutput(-1, err.Error())
	}

	timeout := defaultTimeoutSecs
	if action.TimeoutSecs != nil && *action.TimeoutSecs > 0 {
		timeout = *action.TimeoutSecs
	}

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	output, exitCode := runBash(execCtx, action.Command, action.WorkingDir)

	if execCtx.Err() == context.DeadlineExceeded {
		return errorOutput(-1, "command timed out")
	}
	if execCtx.Err() == context.Canceled {
		return errorOutput(-1, "context cancelled")
	}

	output = truncateIfNeeded(output)

	return &types.CmdOutputObservation{
		BaseEvent: types.BaseEvent{
			Source: types.SourceEnvironment,
		},
		Content:  output,
		ExitCode: exitCode,
	}
}

func validateAction(action *BashAction) error {
	if strings.TrimSpace(action.Command) == "" {
		return fmt.Errorf("command is empty")
	}
	if action.TimeoutSecs != nil && (*action.TimeoutSecs < 1 || *action.TimeoutSecs > maxTimeoutSecs) {
		return fmt.Errorf("timeout_secs out of range")
	}
	if action.WorkingDir != "" {
		info, err := os.Stat(action.WorkingDir)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("working_dir does not exist: %s", action.WorkingDir)
			}
			return fmt.Errorf("working_dir is not accessible: %s", action.WorkingDir)
		}
		if !info.IsDir() {
			return fmt.Errorf("working_dir is not a directory: %s", action.WorkingDir)
		}
	}
	return nil
}

func runBash(ctx context.Context, command, workingDir string) (string, int) {
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if workingDir != "" {
		cmd.Dir = workingDir
	}

	out, err := cmd.CombinedOutput()

	if ctx.Err() == context.DeadlineExceeded {
		return "command timed out", -1
	}
	if ctx.Err() == context.Canceled {
		return "context cancelled", -1
	}

	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	if err != nil {
		if exitCode == 0 {
			exitCode = -1
		}
		return string(out), exitCode
	}

	return string(out), exitCode
}

func truncateIfNeeded(output string) string {
	if len(output) <= maxOutputSize {
		return output
	}
	return output[:maxOutputSize] + "[output truncated]"
}

func errorOutput(exitCode int, msg string) *types.CmdOutputObservation {
	return &types.CmdOutputObservation{
		BaseEvent: types.BaseEvent{
			Source: types.SourceEnvironment,
		},
		Content:  msg,
		ExitCode: exitCode,
	}
}

func init() {
	Register("bash", func(ctx context.Context) Tool {
		return NewBashTool()
	})
}
