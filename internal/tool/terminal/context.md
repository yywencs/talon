# terminal context

## 当前状态

- terminal 基础层已经收拢完成：代码骨架、类型、校验、observation、审计、主流程和基础测试都已进入 `internal/tool/terminal`。
- `internal/tool/terminal_tool.go` 当前只保留兼容入口和工具注册。
- terminal tool 的 action 输入当前已固定为 `command`、`is_input`、`timeout`、`reset`。
- `working_dir` 已不再属于 action，当前作为执行参数存在。
- 当前执行链路已抽象为统一 `TerminalBackend` 接口，`tmux` 与 `PTY` 共用同一套后端能力面。
- 当前默认后端为单个 `tmux session`；`PTY` 已有占位实现和统一接口，但本阶段尚未真正接入。
- 当前已具备最小闭环：参数校验、超时、屏幕读取、输出截断、observation、审计日志、单 `tmux session` 生命周期。
- 当前 `is_input=true` 已支持输入续写、空输入拉输出和 `C-c` 中断映射。
- 当前 `reset=true` 仍返回未实现错误。
- 当前尚未接入 Docker runtime、agent-server 或 remote runtime。

## 未来状态

- 已完成阶段：基础层已收拢，tool 只负责在当前运行环境执行，上层决定运行环境。
- 已完成阶段：持久化 `subprocess` 已实现，连续命令已可以复用同一个 shell。
- 已完成阶段：已基于单个 `tmux session` 打通命令续写、空输入拉输出和基础会话生命周期。
- 下一步 4C：基于同一套 `tmux session` 生命周期实现 `reset=true`，确保旧会话及相关状态被彻底清理。
- 下一步 4D：在单会话语义稳定后演进到池化 `tmux`，支持会话复用、隔离与回收。

## 变更记录

### 2026-04-27

- 新增统一 `TerminalBackend` 接口，收敛 `initialize`、`close`、`send_keys`、`read_screen`、`clear_screen`、`interrupt`、`is_running` 能力面。
- 新增 `tmux` 后端实现，默认执行链路切换到单 `tmux session`。
- 新增 `PTY` 后端占位实现，用于对齐统一接口，但当前仍未真正接入执行链路。
- `Executor` 改为基于后端接口驱动，打通普通命令、`is_input=true` 输入续写、空输入拉输出和 `C-c` 中断映射。
- terminal 单元测试改为优先 mock 后端，符合当前 `spec.md` 的测试约束。

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
