# Tool Specification: bash

## 1. 基础信息

| 字段 | 值 |
|------|-----|
| **Name** | `bash` |
| **Description** | 在宿主机的 shell 环境中执行一条 Bash 命令。命令将在 `os.Exec` 中运行，支持管道、重定向等 bash 特性。返回命令的 stdout/stderr 混合输出及退出码。 |
| **Default Security Risk** | `HIGH` |
| **注册名称** | `bash` |

---

## 2. Action 数据结构设计 (The Input)

```go
type BashAction struct {
    ActionMetadata `json:",inline"`
    Command        string `json:"command" jsonschema:"description=要执行的 bash 命令完整文本,examples=['ls -la','find . -name \"*.go\" | head -20']"`
    TimeoutSecs    int    `json:"timeout_secs,omitempty" jsonschema:"description=命令超时秒数,default=30,minimum=1,maximum=300"`
    WorkingDir     string `json:"working_dir,omitempty" jsonschema:"description=命令执行的工作目录,default=当前进程工作目录,examples=['/tmp','/home/user']"`
}
```

CRITICAL RULE: 所有的具体工具必须通过实例化 &BaseTool[ActionType, Observation]{} 来创建，并提供 Executor 闭包函数。绝对不允许直接在具体工具上实现 Tool 接口处理 rawArgs！

### 必填项与可选项

| 字段 | 必填 | 说明 |
|------|------|------|
| `Command` | **是** | 要执行的完整 bash 命令字符串 |
| `TimeoutSecs` | 否 | 默认 30 秒，最大 300 秒 |
| `WorkingDir` | 否 | 默认为空字符串，表示继承父进程当前工作目录 |

---

## 3. 核心行为与边界条件 (The Behavior)

### 3.1 正常执行主流程

1. **参数解析**：`json.Unmarshal` 解析 `rawArgs` 到 `BashAction` 结构体
2. **参数校验**：
   - `Command` 不得为空字符串
   - `TimeoutSecs` 如果提供，必须在 `[1, 300]` 范围内
   - `WorkingDir` 如果提供，必须是真实存在的目录路径
3. **命令执行**：
   - 通过 `exec.CommandContext(ctx, "bash", "-c", action.Command)` 执行
   - 继承当前进程的所有环境变量。
   - 使用 `bash -c` 而非直接 `sh -c`，确保 bash 特性（管道、重定向、变量展开）可用
   - stdout 和 stderr 合并输出（`exec.Cmd` 默认行为）
4. **结果返回**：
   - 返回 `*types.CmdOutputObservation`，包含合并后的 `Content` 和进程退出码 `ExitCode`
   - 当 `ExitCode == 0` 时表示命令正常退出

### 3.2 必须拦截或处理的异常边界

| 边界场景 | 处理方式 | 返回内容 |
|----------|----------|----------|
| `Command` 为空字符串 | 返回错误 Observation，ExitCode = -1 | `{"error": "command is empty"}` |
| `TimeoutSecs` 超出范围 (< 1 或 > 300) | 返回错误 Observation，ExitCode = -1 | `{"error": "timeout_secs out of range"}` |
| `WorkingDir` 指定但目录不存在 | 返回错误 Observation，ExitCode = -1 | `{"error": "working_dir does not exist: <path>"}` |
| 命令执行超时 | context 被 cancel，返回错误 Observation，ExitCode = -1 | `{"error": "command timed out"}` |
| context 被外部 cancel（如 Agent 中断） | 立即终止进程，返回错误 Observation，ExitCode = -1 | `{"error": "context cancelled"}` |
| 命令输出超过 1MB | 截断输出，末尾附加 `[output truncated]` 标记，ExitCode 保持原值 | 截断后的 Content |
| 命令执行panic（不应发生） | recover 住，返回错误 Observation，ExitCode = -1 | `{"error": "panic recovered: <err>"}` |
| exec.Start 失败（罕见，如路径不存在） | 返回错误 Observation，ExitCode = -1 | `{"error": "failed to start: <err>"}` |

---

## 4. TDD 测试用例规划 (The Test Cases)

### 4.1 Happy Path

| Case 名称 | 输入 | 预期结果 |
|-----------|------|----------|
| `TestBash_SimpleEcho` | `Command: "echo hello"` | `ExitCode == 0`, `Content == "hello\n"` |
| `TestBash_WithTimeout` | `Command: "sleep 1"`, `TimeoutSecs: 5` | `ExitCode == 0`, 正常完成 |
| `TestBash_NonZeroExit` | `Command: "exit 1"` | `ExitCode == 1`, Content 可为空或包含错误信息 |

### 4.2 Edge Cases

| Case 名称 | 输入 | 预期结果 |
|-----------|------|----------|
| `TestBash_EmptyCommand` | `Command: ""` | `ExitCode == -1`, Content 包含 "command is empty" |
| `TestBash_InvalidTimeout` | `Command: "echo hi"`, `TimeoutSecs: 0` | `ExitCode == -1`, Content 包含 "timeout_secs out of range" |
| `TestBash_TimeoutExceeded` | `Command: "sleep 10"`, `TimeoutSecs: 1` | `ExitCode == -1`, Content 包含 "timed out" |
| `TestBash_NonexistentWorkingDir` | `Command: "echo hi"`, `WorkingDir: "/nonexistent/path"` | `ExitCode == -1`, Content 包含 "working_dir does not exist" |
| `TestBash_CtxCancelled` | `Command: "sleep 10"` + 外部 cancel | `ExitCode == -1`, Content 包含 "context cancelled" |
| `TestBash_OutputTruncation` | `Command: "yes | head -c 2000000"`（2MB输出） | `ExitCode == 0`, Content 长度 <= 1MB + 截断标记 |

### 4.3 生命周期与并发测试

| Case 名称 | 场景描述 | 预期结果 |
|-----------|----------|----------|
| `TestBash_ConcurrentExec` | 同一 `BashTool` 实例并发执行 10 个不同命令 | 所有结果正确对应，无竞态 |
| `TestBash_CtxCancellationMidExec` | 命令执行中途 context 被 cancel | 命令被杀死，返回 "context cancelled" |
| `TestBash_DependencyInjection` | 使用 mock/exec 可以验证不同场景 | 行为符合预期 |

---

## 5. 实现约束

- 必须实现 `Tool` 接口（`Name()`, `Description()`, `Execute(ctx context.Context, rawArgs []byte) Observation`）
- 必须通过 `tool.Register("bash", func(ctx context.Context) tool.Tool { return NewBashTool() })` 注册
- 所有错误必须通过返回的 `Observation` 传递，不得 panic
- `SecurityRisk` 固定为 `SecurityRisk_HIGH`，不提供配置选项（高风险工具不应降低告警级别）
