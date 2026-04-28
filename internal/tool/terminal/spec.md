# terminal 4F tmux fixed-pane interaction SPEC

## Scope

- 本文件只约束 `4F`：将当前共享 `tmux session` 下的 `pane` 生命周期，从“空闲队列池化复用”收敛为“按逻辑 `pane_id` 长期固定绑定同一个 `pane`”。
- `4F` 的目标是：同一 tool 实例继续只维护一个共享 `tmux session`，但每个逻辑 `pane_id` 在该 session 内长期绑定独立 `window/panel`；连续普通命令、`is_input=true`、空输入拉输出和 `C-c` 中断都稳定命中同一个底层 `pane`。
- `4F` 的核心问题是：普通命令完成后不再归还 `pane` 到公共池，而是保留原绑定，让同一条逻辑终端链路持续保留 shell 上下文、工作目录、环境变量和交互语义。
- `4F` 仍保持多活能力：不同逻辑 `pane_id` 可以同时存在，各自绑定独立 `window/panel` 并发执行、并发续写、并发拉输出与局部 reset。
- `4F` 仍保持“上层决定运行环境，tool 只在当前运行环境执行”的边界，不要求区分宿主机、Docker 或 remote，也不要求引入 `PTY` 新能力。

## Constraints

- 工具名必须继续是 `bash`。
- action 输入字段继续使用 `command`、`pane_id`、`is_input`、`timeout`、`reset`；`pane_id` 必须由上层显式传入，且表示稳定的逻辑终端链路标识，而不是底层 `tmux` pane id。
- 未提供 `pane_id` 的调用必须返回稳定错误；禁止依赖“当前唯一活跃命令”之类的隐式猜测路由。
- `working_dir` 必须继续作为当前运行环境参数或共享 session / 新建 pane 初始化参数存在，不得回流到 action。
- 同一 tool 实例上的连续调用，在 `reset=false` 时必须继续优先复用同一个共享 `tmux session`，不得退回到“一条命令启动一个新 shell”的语义。
- 内部状态必须收敛为：共享 `session` 状态、`pane_id -> pending` 状态映射、`pane_id -> window/panel` 长期绑定状态映射。
- 不得再维护“空闲 `panel` 队列”“最大空闲数”“归还后等待复用”这类公共池化状态；普通命令完成后，其底层 `pane` 必须继续归属于原 `pane_id`。
- 同一个逻辑 `pane_id` 在任意时刻最多只允许绑定一个底层 `window/panel`；同一个逻辑 `pane_id` 不得并发创建第二条新的 pending 交互链路。
- 不同逻辑 `pane_id` 之间必须允许并发；多个普通命令可以在同一个共享 `session` 的不同固定 `window/panel` 上同时运行。
- 当 `is_input=false` 且 `reset=false` 时，普通命令必须基于当前逻辑 `pane_id` 获取其长期绑定的 `window/panel`；若该 `pane_id` 尚未绑定 pane，才允许在共享 session 下创建新的 `window/panel`。
- 普通命令执行完成后，只能清理该 `pane_id` 的 pending 状态，不得销毁固定 `pane`，也不得解除绑定。
- 当 `is_input=true` 且 `reset=false` 且 `command` 非空时，必须把输入发送到当前逻辑 `pane_id` 已绑定的同一个固定 `window/panel` 前台进程，而不是新开 shell 执行。
- 当 `is_input=true` 且 `reset=false` 且 `command` 为空时，必须允许基于当前逻辑 `pane_id` 空输入拉输出。
- 当 `is_input=true` 且 `command` 为 `C-c`、`^C` 或等价中断指令时，必须映射为对当前逻辑 `pane_id` 绑定固定 `window/panel` 前台进程的中断，而不是把字面文本输入给 shell。
- 若当前逻辑 `pane_id` 不存在活跃交互链路，则 `is_input=true` 且 `command` 非空必须返回稳定错误；`is_input=true` 且 `command` 为空时可以返回空输出，但不得隐式创建新命令或隐式分配新 `panel`。
- 同一条逻辑 `pane_id` 链路内部，工作目录、环境变量和 shell 状态必须在其绑定的固定 `window/panel` 中持续可见；不同逻辑 `pane_id` 之间不得共享未清理状态。
- 所有 `ReadScreen`、`SendKeys`、`Interrupt`、`PanePID`、`CurrentWorkingDir` 都必须稳定命中当前 `pane_id` 绑定的固定 pane；禁止在绑定不存在时回退到其他 pane。
- `reset=true` 必须只表示 pane 级局部 reset：只清理当前 `pane_id` 的 pending 状态和固定绑定，并销毁该底层 `pane`；不得影响其他逻辑 `pane_id`。
- reset 后同一个 `pane_id` 再次收到普通命令时，必须创建全新的 `window/panel`，而不是恢复旧 pane 或借用其他 `pane_id` 的 pane。
- 若共享 `tmux session` 意外丢失或后端状态损坏，允许内部触发全局清理和重建，但不得将这种能力暴露为 action 字段。
- `TerminalObservation` 的基础语义必须保持兼容，至少包括 `exit_code`、`timeout`、`metadata.pid`、`metadata.working_dir`。
- 若当前运行环境缺少 `tmux`，必须返回稳定错误；所有失败路径必须返回稳定 observation error，禁止 panic。
- 当前基础审计日志必须保持可用，且至少能定位共享 session、目标逻辑 `pane_id`、目标固定 `window/panel` 和 reset / 重建动作。
- 导出元素和文档注释必须使用中文，并符合 GoDoc 规范。
- 新增或删除 terminal 目录文件后，必须同步更新 `context.md`。

## Definition of Done

- 同一 tool 实例在 `reset=false` 的连续普通命令调用下，始终复用同一个共享 `tmux session`。
- 同一个逻辑 `pane_id` 的连续普通命令调用，会始终命中同一个固定 `window/panel`，除非显式 `reset=true` 或底层 session 整体丢失后被内部重建。
- 同一个逻辑 `pane_id` 的跨命令 shell 状态保持稳定，至少包括工作目录、环境变量和同一 shell 上下文的连续可见性。
- 至少存在两个不同逻辑 `pane_id` 的普通命令能够在同一个共享 `tmux session` 的不同固定 `window/panel` 上同时运行，而不会相互阻塞或抢占同一个 pane。
- 至少存在两条不同逻辑 `pane_id` 的需要后续输入的命令链路；后续 `is_input=true` 能继续命中各自原始固定 `window/panel`。
- 至少存在一次空输入拉输出和一次 `C-c` 中断，都能按逻辑 `pane_id` 命中对应固定 `window/panel`。
- 任意一个逻辑 `pane_id` 的交互命令完成或被中断后，pending 状态会被清理，但固定 pane 绑定仍然保留，可继续用于后续普通命令。
- 默认 `reset=true` 后只会清理当前逻辑 `pane_id` 对应的活跃链路和固定 pane 绑定；其他逻辑 `pane_id` 的活跃命令与输入续写能力不受影响。
- 同一个 `pane_id` 在 reset 后再次执行普通命令时，会得到全新的底层 pane，而不是旧 pane 或其他逻辑 `pane_id` 的 pane。
- 底层共享 `tmux session` 意外丢失后，后续普通命令调用能够按统一策略恢复，不会留下错误的固定绑定状态。
- `TerminalObservation` 返回结构保持兼容，且 `context.md` 已更新为最新真实状态。

## Test Contract

- 单元测试必须优先通过 mock `tmux` 适配层或命令执行层完成，禁止把真实 `tmux` 依赖作为单元测试前提。
- 如需补充真实 `tmux` 的集成或 live 测试，必须默认跳过，并通过显式环境变量开启。
- 共享 session 基线测试：连续普通命令始终复用同一个共享 `tmux session`，且验证一次 session 丢失后的重建路径。
- 固定 pane 绑定测试：同一个 `pane_id` 首次执行会创建 pane，后续普通命令继续命中同一个固定 pane，命令完成后绑定仍然存在。
- 跨命令状态保持测试：同一个 `pane_id` 上一条命令中的 `cd`、`export` 或等价 shell 状态修改，在下一条普通命令中仍然可见；不同 `pane_id` 之间不会共享状态。
- 输入续写测试：`is_input=true` 的续写、空输入拉输出、`C-c` 中断都必须命中原固定 pane，且不同 `pane_id` 之间不会串线。
- reset 测试：`reset=true` 只影响当前 `pane_id`，reset 后旧交互链路不可继续续写，下一条普通命令必须创建全新 pane。
- 元数据与路由测试：`PanePID`、`CurrentWorkingDir`、`ReadScreen`、`SendKeys`、`Interrupt` 只会命中当前 `pane_id` 对应固定 pane，不会回退到其他 pane。
- 超时与错误测试：固定 pane 模式下超时语义正确；分配失败、写入失败、拉输出失败、中断失败、结果解析失败或 `pane_id` 路由失败时返回稳定错误，且不会留下悬空绑定或误清理其他 `pane_id`。
