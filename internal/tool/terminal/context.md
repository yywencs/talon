# terminal context

## 当前状态

- terminal 基础层已经收拢完成：代码骨架、类型、校验、observation、审计、主流程和基础测试都已进入 `internal/tool/terminal`。
- `internal/tool/terminal_tool.go` 当前只保留兼容入口和工具注册。
- terminal tool 的 action 输入当前已固定为 `command`、`is_input`、`timeout`、`reset`。
- `working_dir` 已不再属于 action，当前作为执行参数存在。
- 当前 `bash` 在“当前进程所在的运行环境”中直接执行 `bash -c`。
- 当前已具备最小闭环：参数校验、超时、输出收集、截断、observation、审计日志。
- 当前尚未实现真正的 session；`is_input` 和 `reset` 已建字段但仍返回未实现错误。
- 当前尚未接入 Docker runtime、agent-server 或 remote runtime。

## 未来状态

- 已完成阶段：基础层已收拢，tool 只负责在当前运行环境执行，上层决定运行环境。
- 下一步 4A：先实现持久化 `subprocess`，让连续命令可以复用同一个 shell。
- 下一步 4B：在持久化 shell 之上接入 `PTY`，让交互式输入输出更接近真实终端。
- 下一步 4C：基于 `subprocess + PTY` 真正实现 `is_input=true` 的输入续写和空输入拉输出。
- 下一步 4D：基于同一套 session 生命周期实现 `reset=true`，确保旧 shell 状态被彻底清理。
- 下一步 4E：完成后再评估如何与 Docker workspace / agent-server 调用链对齐。

## 变更记录

### 2026-04-26

- 初始化 `internal/tool/terminal/context.md`。
- 新增 `internal/tool/terminal/spec.md`。
- 新增第一阶段 terminal 代码骨架和基础测试。
- terminal 相关主逻辑迁入 `internal/tool/terminal`。
- `internal/tool/terminal_tool.go` 收敛为兼容入口。
- 第二阶段将 action 输入收敛为 `command`、`is_input`、`timeout`、`reset`。
- `working_dir` 从 action 中移出并下沉到 executor 初始化参数。
- 第三阶段方向修正为“上层控制运行环境，tool 只在当前环境执行”。
- 第四阶段拆分为 `subprocess`、`PTY`、`is_input`、`reset` 四个子任务，避免一次做完整 session 语义。
- 收敛本文件职责：只记录当前状态、未来状态和变更记录。
