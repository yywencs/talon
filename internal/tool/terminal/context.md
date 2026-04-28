# terminal context
## 各文件作用

- `types.go`：定义 terminal tool 的 action、observation 和基础常量。
- `validate.go`：校验 action 参数和 `working_dir`。
- `execute.go`：terminal 主执行编排，负责普通命令、`is_input`、`reset` 和 pending 生命周期。
- `backend.go`：定义统一 `TerminalBackend` 接口和可选扩展能力。
- `tmux_backend.go`：`tmux` 后端对外壳层，负责初始化入口和基础调用转发。
- `tmux_pane_pool.go`：共享 `tmux session` 下固定 `pane` 绑定、pane 分配与局部 reset 的核心实现。
- `observe.go`：构造 terminal observation 和错误 observation。
- `errors.go`：定义 terminal 内部结构化错误和稳定错误文本。
- `audit.go`：生成命令审计字段并记录执行完成日志。
- `terminal_test.go`：覆盖 terminal 主流程、交互链路和通用 backend 语义测试。
- `tmux_backend_fixed_pane_test.go`：覆盖 `tmux` 固定 `pane` 绑定、局部 reset 和元数据路由测试。
- `spec.md`：记录当前阶段实现契约、约束和测试要求。
- `context.md`：记录 terminal 当前真实状态、下一步方向和文件索引。


## 当前状态

- terminal 基础层已经收拢完成：代码骨架、类型、校验、observation、审计、主流程和基础测试都已进入 `internal/tool/terminal`。
- `internal/tool/terminal_tool.go` 当前只保留兼容入口和工具注册。
- terminal tool 的 action 输入已扩展为 `command`、`pane_id`、`is_input`、`timeout`、`reset`。
- `working_dir` 已不再属于 action，当前作为执行参数存在。
- 当前执行链路已抽象为统一 `TerminalBackend` 接口，`tmux` 与 `PTY` 共用同一套后端能力面。
- 当前默认后端已从“共享 `tmux session` + 空闲队列池化复用”收敛为“共享 `tmux session` + `pane_id` 长期固定绑定同一个 `window/panel`”模型；`PTY` 已有占位实现和统一接口，但本阶段尚未真正接入。
- 当前固定 `pane` 主逻辑已集中收敛到 `internal/tool/terminal/tmux_pane_pool.go`，`tmux_backend.go` 主要保留对外 backend 壳层和 runner 适配。
- 当前已具备最小闭环：参数校验、超时、屏幕读取、输出截断、observation、审计日志、共享 `tmux session` 生命周期、固定 `window/panel` 分配与局部重建。
- 当前普通命令执行前会通过 backend 生命周期接口确保当前 `pane_id` 已绑定固定 `panel`；命令完成后只清理 pending 状态，不回收该 `panel`；异常路径或 `reset=true` 会销毁失效 `panel` 并解除绑定。
- 当前 `Executor` 内部已将单个 pending 状态升级为 `map[string]*pendingExecution`，并对每个逻辑 `pane_id` 使用独立锁保护；不同 `pane_id` 的命令链路可以并发执行。
- 当前 `TmuxBackend` 内部已维护稳定的 `pane_id -> window/panel` 固定绑定映射；不同 `pane_id` 可以各自绑定独立 `panel` 并发交互。
- 当前 `is_input=true` 已稳定绑定到对应逻辑 `pane_id` 的同一个 `window/panel`：输入续写会写入各自前台进程，空输入可只拉取各自新增输出，`C-c` 会映射到对应 pane 中断。
- 当前 `reset=true` 已落地为 pane 级局部 reset：默认只清理当前 `pane_id` 的 pending 状态和 pane 绑定，不影响其他活跃 pane。
- 当前 `reset=true` 已支持“仅重置”和“先重置再执行普通命令”两种稳定语义；若与 `is_input=true` 同时出现，则不会再续写旧前台进程。
- 当前同一个逻辑 `pane_id` 的连续普通命令会持续命中同一个固定 shell，上下文、工作目录和环境变量可以跨命令保留；`reset=true` 后才会为该 `pane_id` 重建全新 shell。
- 当前多个 agent 或多个任务若共享同一个 `Executor/backend`，已经可以通过稳定的逻辑 `pane_id` 各自绑定独立活跃 pane 并发交互。
- 当前若共享 `tmux session` 意外丢失或后端状态损坏，tool 仍可在内部触发整体清理和重建；该能力不再暴露为 action 字段。
- 当前尚未接入 Docker runtime、agent-server 或 remote runtime。

## 未来状态

- 已完成阶段：terminal 主语义已从“共享 `tmux session` + 空闲 `pane` 池化复用”收敛为“共享 `tmux session` + `pane_id` 长期固定绑定同一个 `pane`”，同一条逻辑终端链路可以持续保留 shell 上下文。
- 已完成阶段：backend 状态模型已去掉 `idlePanes`、`maxIdlePanes` 和“完成后归还队列”的设计，改为显式维护 `pane_id -> tmuxPaneHandle` 的长期绑定关系。
- 已完成阶段：命令生命周期接口语义已调整为固定绑定模式，`PrepareCommand` 负责确保绑定，`CompleteCommand` 只清理 pending，`InvalidateCommand` / `ResetPane` 负责销毁旧 pane 并解除绑定。
- 已完成阶段：元数据和读写路由已修正为只命中当前 `pane_id` 绑定的固定 pane，不再从空闲队列或其他 pane 做 fallback。
- 已完成阶段：测试已新增独立的固定 `pane` backend 测试文件，覆盖固定绑定、局部 reset 和元数据不串 pane 的关键语义。
- 下一步：补齐更多真实 `tmux` 集成验证与可观测性元数据，确认固定 `pane` 绑定、局部 reset 和共享 session 整体恢复路径在真实环境下也足够稳定。
