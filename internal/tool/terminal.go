package tool

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/wen/opentalon/internal/types"
)

type CmdOutputMetadata struct {
	PID        *int   `json:"pid,omitempty"`
	WorkingDir string `json:"working_dir,omitempty"`
}

type TerminalObservation struct {
	types.BaseObservation
	Command           *string           `json:"command,omitempty"`
	ExitCode          *int              `json:"exit_code,omitempty"`
	Timeout           bool              `json:"timeout"`
	Metadata          CmdOutputMetadata `json:"metadata"`
	FullOutputSaveDir *string           `json:"full_output_save_dir,omitempty"`
}

func (o *TerminalObservation) OutputText() string {
	if o == nil {
		return ""
	}
	return types.FlattenTextContent(o.GetContent())
}

func (o *TerminalObservation) ExitCodeValue() int {
	if o == nil || o.ExitCode == nil {
		return 0
	}
	return *o.ExitCode
}

type BashAction struct {
	ActionMetadata `json:",inline"`
	Command        string `json:"command" jsonschema:"description=要执行的 bash 命令完整文本,examples=[\"ls -la\",\"find . -name *.go | head -20\"]"`
	TimeoutSecs    *int   `json:"timeout_secs,omitempty" jsonschema:"description=命令超时秒数,default=30,minimum=1,maximum=300"`
	WorkingDir     string `json:"working_dir,omitempty" jsonschema:"description=命令执行的工作目录,default=当前进程工作目录,examples=[\"/tmp\",\"/home/user\"]"`
}

const (
	defaultTimeoutSecs = 30
	maxTimeoutSecs     = 300
	maxOutputSize      = 1024 * 1024
)

func bashExecutor(ctx context.Context, action BashAction) *TerminalObservation {
	if err := validateAction(&action); err != nil {
		return errorOutput(action.Command, action.WorkingDir, nil, false, -1, err.Error())
	}

	timeout := defaultTimeoutSecs
	if action.TimeoutSecs != nil && *action.TimeoutSecs > 0 {
		timeout = *action.TimeoutSecs
	}

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	result := runBash(execCtx, action.Command, action.WorkingDir)
	result.Output = truncateIfNeeded(result.Output)
	return NewTerminalObservation(action.Command, action.WorkingDir, result.PID, result.TimedOut, result.ExitCode, result.Output)
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

type commandResult struct {
	Output   string
	ExitCode int
	TimedOut bool
	PID      *int
}

func runBash(ctx context.Context, command, workingDir string) commandResult {
	cmd := exec.Command("bash", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if workingDir != "" {
		cmd.Dir = workingDir
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return commandResult{Output: fmt.Sprintf("failed to create stdout pipe: %v", err), ExitCode: -1}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return commandResult{Output: fmt.Sprintf("failed to create stderr pipe: %v", err), ExitCode: -1}
	}

	if err := cmd.Start(); err != nil {
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
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
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

func truncateIfNeeded(output string) string {
	if len(output) <= maxOutputSize {
		return output
	}
	return output[:maxOutputSize] + "[output truncated]"
}

func errorOutput(command, workingDir string, pid *int, timeout bool, exitCode int, msg string) *TerminalObservation {
	return NewTerminalObservation(command, workingDir, pid, timeout, exitCode, msg)
}

func NewTerminalObservation(command, workingDir string, pid *int, timeout bool, exitCode int, output string) *TerminalObservation {
	obs := &TerminalObservation{
		BaseObservation: types.BaseObservation{
			BaseEvent: types.BaseEvent{
				Source: types.SourceEnvironment,
			},
			Content: []types.Content{
				types.TextContent{
					DataType: types.ContentTypeText,
					Text:     output,
				},
			},
			ErrorStatus: timeout || exitCode != 0,
		},
		Command:  stringPtr(command),
		Timeout:  timeout,
		Metadata: CmdOutputMetadata{PID: pid, WorkingDir: workingDir},
	}
	if exitCode != 0 || output != "" || pid != nil {
		obs.ExitCode = intPtr(exitCode)
	}
	return obs
}

func intPtr(v int) *int {
	return &v
}

func stringPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func newBashTool() *BaseTool[BashAction, *TerminalObservation] {
	return &BaseTool[BashAction, *TerminalObservation]{
		ToolName: "bash",
		ToolDesc: "在宿主机的 shell 环境中执行一条 Bash 命令。命令将在 `os.Exec` 中运行，支持管道、重定向等 bash 特性。返回命令的 stdout/stderr 混合输出及退出码。",
		Executor: bashExecutor,
	}
}

func NewBashTool() *BaseTool[BashAction, *TerminalObservation] {
	return newBashTool()
}

func init() {
	Register("bash", func(ctx context.Context) Tool {
		return newBashTool()
	})
}
