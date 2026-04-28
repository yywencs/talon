# terminal context

## 当前状态

- terminal 基础层已经收拢完成：代码骨架、类型、校验、observation、审计、主流程和基础测试都已进入 `internal/tool/terminal`。
- `internal/tool/terminal_tool.go` 当前只保留兼容入口和工具注册。
- terminal tool 的 action 输入当前已固定为 `command`、`is_input`、`timeout`、`reset`。
- `working_dir` 已不再属于 action，当前作为执行参数存在。
- 当前执行链路已抽象为统一 `TerminalBackend` 接口，`tmux` 与 `PTY` 共用同一套后端能力面。
- 当前默认后端已从“单 `tmux session` 单 pane”演进为“一个共享 `tmux session` + 多个 `window` + 一个 `window` 对应一个 `panel`”的池化模型；`PTY` 已有占位实现和统一接口，但本阶段尚未真正接入。
- 当前 `tmux` 池化主逻辑已集中收敛到 `internal/tool/terminal/tmux_pane_pool.go`，`tmux_backend.go` 主要保留对外 backend 壳层和 runner 适配。
- 当前已具备最小闭环：参数校验、超时、屏幕读取、输出截断、observation、审计日志、共享 `tmux session` 生命周期、`window/panel` 分配与回收。
- 当前普通命令执行前会通过 backend 生命周期接口分配命令 `panel`；命令完成后会回收该 `panel`，异常路径会丢弃失效 `panel`。
- 当前空闲 `panel` 默认通过 FIFO 队列缓存，默认长度为 `5`；队列满时会淘汰最早进入队列的空闲 `panel` 并销毁其对应 `window`。
- 当前 `is_input=true` 已稳定绑定到活跃命令所在的同一个 `window/panel`：输入续写会写入同一前台进程，空输入可只拉取新增输出，`C-c` 会映射到当前 pane 中断。
- 当前 `reset=true` 已基于同一套共享 `tmux session` 生命周期落地：会清理旧 session、丢弃旧 pending 状态、清空空闲 `panel` 队列，并在后续普通命令调用时重建干净 session。
- 当前 `reset=true` 已支持“仅重置”和“先重置再执行普通命令”两种稳定语义；若与 `is_input=true` 同时出现，则不会再续写旧前台进程。
- 当前复用 `panel` 时会优先通过 `respawn-pane` 回到干净 shell 基线，再重新执行 shell 初始化，避免旧前台进程和历史状态泄漏到新命令。
- 当前尚未接入 Docker runtime、agent-server 或 remote runtime。

## 未来状态

- 已完成阶段：基础层已收拢，tool 只负责在当前运行环境执行，上层决定运行环境。
- 已完成阶段：持久化 `subprocess` 已实现，连续命令已可以复用同一个 shell。
- 已完成阶段：已基于单个 `tmux session` 打通命令续写、空输入拉输出和基础会话生命周期。
- 已完成阶段：已基于同一套 `tmux session` 生命周期实现 `reset=true`，旧会话及相关状态会被清理。
- 已完成阶段：已将 `is_input=true` 输入续写、空输入拉输出和 `C-c` 中断映射稳定支持到 `tmux session`。
- 已完成阶段：已将 `tmux` 后端演进到共享 `session` 的 `window/panel` 池化模型，支持 pane 复用、隔离与回收。
- 下一步：补齐更细的池状态可观测性与更多真实 `tmux` 集成验证，确认共享 session 丢失、pane 复用失败和极端回收路径在真实环境下也足够稳定。

## 变更记录

### 2026-04-27

- 新增 `internal/tool/terminal/tmux_pane_pool.go`，集中实现共享 `tmux session`、`window/panel` 分配、pane 复用和空闲队列回收逻辑。
- `TmuxBackend` 已改为通过共享 `tmux session` 驱动命令执行；普通命令会先分配 pane，完成后回收 pane，异常时丢弃 pane。
- 空闲 `panel` 队列默认长度固定为 `5`，队列溢出时会淘汰最旧空闲项并销毁其对应 `window`。
- `Executor` 已接入 backend 命令生命周期：普通命令开始前分配 pane，命令完成后回收 pane，读取/发送/解析失败时失效当前 pane。
- `is_input=true`、空输入拉输出和 `C-c` 中断已继续绑定到同一个活跃 `window/panel`，未因池化改动而退回到新 shell 语义。
- terminal 已补充池化相关测试，覆盖空闲 pane 复用和队列溢出淘汰最旧 pane 的行为。
- 新增统一 `TerminalBackend` 接口，收敛 `initialize`、`close`、`send_keys`、`read_screen`、`clear_screen`、`interrupt`、`is_running` 能力面。
- 新增 `tmux` 后端实现，默认执行链路切换到单 `tmux session`。
- 新增 `PTY` 后端占位实现，用于对齐统一接口，但当前仍未真正接入执行链路。
- `Executor` 改为基于后端接口驱动，打通普通命令、`is_input=true` 输入续写、空输入拉输出和 `C-c` 中断映射。
- `Executor` 已实现 `reset=true`：支持关闭旧 session、清空 pending 状态，并在后续普通命令调用时重建干净 session。
- `reset=true` 与 `is_input=true` 组合时不再续写旧前台进程；如需继续交互，必须重新启动新的命令链路。
- `Executor` 已补强 pending 生命周期：命令已结束、错误路径和 reset 路径都会清理不可继续复用的旧 pending 状态。
- 空输入拉输出已按 pending 增量消费输出，不会重复回放已经消费过的旧输出。
- terminal 已新增结构化错误定义，执行链路中的状态错误、后端错误和执行错误会先构造成 error，再统一映射为 observation 文本。
- 普通命令、输入续写、空输入拉输出和 `C-c` 中断在单 `tmux session` 下的协同行为已补齐测试覆盖。
- `tmux` 后端 `Close()` 已对“session 已不存在”场景做幂等处理。
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
