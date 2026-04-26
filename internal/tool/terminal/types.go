package terminal

import (
	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/utils"
)

// CmdOutputMetadata 表示终端执行结果中的补充元信息。
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

// ActionType 返回终端动作类型。
func (a *TerminalAction) ActionType() types.ActionType {
	return types.ActionRun
}

// TerminalObservation 表示终端工具返回的观察结果。
type TerminalObservation struct {
	types.BaseObservation
	Command           *string           `json:"command,omitempty"`
	ExitCode          *int              `json:"exit_code,omitempty"`
	Timeout           bool              `json:"timeout"`
	Metadata          CmdOutputMetadata `json:"metadata"`
	FullOutputSaveDir *string           `json:"full_output_save_dir,omitempty"`
}

// OutputText 返回 observation 中的文本输出。
func (o *TerminalObservation) OutputText() string {
	if o == nil {
		return ""
	}
	return utils.FlattenTextContent(o.GetContent())
}

// ExitCodeValue 返回 observation 中的退出码。
func (o *TerminalObservation) ExitCodeValue() int {
	if o == nil || o.ExitCode == nil {
		return 0
	}
	return *o.ExitCode
}

// BashTool 定义 bash 工具的输入参数。
type BashTool struct {
	types.ToolMetadata `json:",inline"`
	Command            string   `json:"command" jsonschema:"description=要执行的 bash 命令文本,examples=[\"ls -la\",\"find . -name *.go | head -20\"]"`
	IsInput            bool     `json:"is_input,omitempty" jsonschema:"description=是否向运行中的进程发送输入,default=false"`
	Timeout            *float64 `json:"timeout,omitempty" jsonschema:"description=命令超时时间(秒),default=30,minimum=0.001,maximum=300"`
	Reset              bool     `json:"reset,omitempty" jsonschema:"description=是否重置终端会话,default=false"`
}

const (
	defaultTimeoutSecs = 30
	maxTimeoutSecs     = 300
	maxOutputSize      = 1024 * 1024
)

// ToolDescription 是 bash 工具的对外描述文本。
const ToolDescription = `# 命令执行相关规则完整翻译
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
