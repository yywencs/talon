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
	"github.com/wen/opentalon/pkg/utils"
)

type CmdOutputMetadata struct {
	PID        *int   `json:"pid,omitempty"`
	WorkingDir string `json:"working_dir,omitempty"`
}

// TerminalAction 对应大模型发出的终端操作指令。
type TerminalAction struct {
	Command string   `json:"command"`
	IsInput bool     `json:"is_input,omitempty"`
	Timeout *float64 `json:"timeout,omitempty"`
	Reset   bool     `json:"reset,omitempty"`
}

func (a *TerminalAction) ActionType() types.ActionType {
	return types.ActionRun
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
	return utils.FlattenTextContent(o.GetContent())
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

func (a BashAction) ActionType() types.ActionType {
	return types.ActionRun
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
					Text: output,
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

const (
	TOOL_DESCRIPTION = `# 命令执行相关规则完整翻译
	### 命令执行
	* 每次一条命令：每次只能执行一条 bash 命令。如果需要依次运行多条命令，使用 "&&" 或 ";" 串联执行。
	* 会话持久化：命令在持久化的 shell 会话中执行，环境变量、虚拟环境、工作目录在命令之间会保持不变。
	* 软超时：命令设有 10 秒软超时，达到时限后，你可以选择继续或中断该命令（详见下方章节）。
	* Shell 选项：请勿在当前环境的 shell 脚本或命令中使用 "set -e"、"set -eu" 或 "set -euo pipefail"。运行环境可能不支持这些配置，并会导致 shell 会话不可用。如果需要执行多行 bash 命令，请将命令写入文件后再运行。

	### 长时间运行的命令
	* 对于可能无限期运行的命令，将其放到后台执行并将输出重定向到文件，例如："python3 app.py > server.log 2>&1 &"。
	* 对于可能长时间运行的命令（如安装或测试命令），或固定时长运行的命令（如 sleep），你应该为函数调用设置合适的 "timeout"（超时）参数。
	* 如果 bash 命令返回退出码 "-1"，表示进程触发了软超时但尚未执行完毕。通过设置 "is_input=true"，你可以：
	- 发送空的 "command" 以获取更多日志
	- 向正在运行的进程标准输入（STDIN）发送文本（将 "command" 设为对应文本）
	- 发送控制命令如 "C-c"（Ctrl+C）、"C-d"（Ctrl+D）或 "C-z"（Ctrl+Z）来中断进程
	- 如果使用了 "C-c"，可以用更长的超时参数重新启动进程，使其完整运行

	### 最佳实践
	* 目录校验：在创建新目录或文件前，先确认父目录存在且位置正确。
	* 目录管理：尽量使用绝对路径维持工作目录，避免频繁使用 "cd"。

	### 输出处理
	* 输出截断：如果输出超过最大长度，会在返回前被截断。

	### 终端重置
	* 终端重置：如果终端失去响应，可以设置 "reset=true" 来创建新的终端会话。这会终止当前会话并全新启动。
	* 警告：重置终端会丢失所有已设置的环境变量、工作目录变更以及正在运行的进程。仅在终端无法响应命令时使用。`
)

func newBashTool() *BaseTool[BashAction, *TerminalObservation] {
	return &BaseTool[BashAction, *TerminalObservation]{
		ToolName: "bash",
		ToolDesc: TOOL_DESCRIPTION,
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
