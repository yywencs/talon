# terminal 4E tmux pool SPEC

## Scope

- 本文件只约束 `4E`：在当前单 `tmux session` 语义稳定的基础上，演进到共享 `session` 的池化 `tmux` 执行模型。
- `4E` 的目标是：同一 tool 实例下只维护一个共享 `tmux session`，在该 session 内管理多个 `window`，并约定一个 `window` 只对应一个可执行 `panel`。
- `4E` 的核心问题是：普通命令、`is_input=true` 输入续写、空输入拉输出、`C-c` 中断，以及执行完成后的资源回收，都必须绑定到共享 `session` 内的正确 `window/panel`。
- `4E` 需要补齐池化能力：活跃中的 `panel` 可持续交互，已使用完毕的 `panel` 不直接销毁，而是进入空闲队列等待复用。
- `4E` 需要补齐隔离能力：不同命令链路之间不能因为复用同一 `panel` 而泄漏旧的前台进程、旧 shell 状态或不可预测的残留环境。
- `4E` 仍保持“上层决定运行环境，tool 只在当前运行环境执行”这一边界。
- `4E` 不要求在 tool 层区分宿主机、Docker 或 remote。
- `4E` 不要求引入 `PTY` 新能力或切换默认后端。
- `4E` 不要求修改 `TerminalObservation` 的基础返回结构。

## Constraints

- 工具名必须继续是 `bash`。
- action 输入字段继续保持 `command`、`is_input`、`timeout`、`reset` 四个字段。
- `working_dir` 必须继续作为当前运行环境参数或池化 session 初始化参数存在，不得回流到 action。
- session 必须继续由 tool 层持有或可访问，但不得要求上层 action 显式传 `session_id`、`window_id` 或 `panel_id`。
- 同一 tool 实例上的连续调用，在 `reset=false` 时必须继续优先复用同一个共享 `tmux session`，不得退回到“一条命令启动一个新 shell”的语义。
- `tmux` 必须继续成为会话主载体；`PTY` 仍不是本阶段实现前提。
- 池化模型必须固定为“一个共享 `tmux session` + 多个 `window` + 一个 `window` 对应一个 `panel`”；本阶段不得在同一个 `window` 中再拆分多个并发 panel。
- tool 内部必须显式维护至少三类状态：共享 `session` 状态、活跃交互链路到 `window/panel` 的绑定状态、以及可复用空闲 `panel` 队列状态。
- 空闲 `panel` 队列默认长度必须为 `5`；该默认值可以在内部实现中抽象为常量或配置，但不得要求上层 action 显式传入。
- 已执行完成且可复用的 `panel` 不得立即销毁，必须按队列顺序进入空闲队列等待复用。
- 当空闲 `panel` 队列已满又有新的 `panel` 需要归还时，必须回收并销毁最早进入队列的空闲 `panel`，再将最新空闲 `panel` 入队；禁止无限增长。
- 当 `is_input=false` 且 `reset=false` 时，普通命令必须从共享 `session` 中获取一个可执行 `window/panel` 执行；优先复用空闲队列中的 `panel`，无可用项时才创建新的 `window/panel`。
- 新创建的 `window/panel` 必须挂在当前共享 `tmux session` 下；不得因为池化而为单条命令额外创建第二个共享 session。
- 普通命令一旦启动并留下活跃前台进程，tool 必须显式记录当前存在一条可续写的交互链路，并将其唯一绑定到启动该命令的 `window/panel`。
- 当 `is_input=true` 且 `reset=false` 且 `command` 非空时，必须将 `command` 作为输入发送到当前交互链路绑定的同一个 `window/panel` 前台进程，而不是新开 shell 执行，也不是作为新的普通命令包装执行。
- 当 `is_input=true` 且 `reset=false` 且 `command` 为空时，必须允许“空输入拉输出”，即不发送新输入也能读取当前绑定 `window/panel` 的新增输出。
- 空输入拉输出必须只观察当前活跃交互链路对应 `window/panel` 的新增输出，不得错误重复返回旧屏幕全量内容，也不得误判为启动了新命令。
- 当 `is_input=true` 且 `command` 为 `C-c`、`^C` 或等价中断指令时，必须映射为对当前绑定 `window/panel` 前台进程的中断，而不是把字面文本输入给 shell。
- `C-c` 中断成功后，旧交互链路必须被视为已终止；若 shell 已回到提示态，对应 `panel` 只能在完成清理并满足复用条件后回收到空闲队列。
- 若当前不存在活跃交互链路，则 `is_input=true` 且 `command` 非空必须返回稳定错误；`is_input=true` 且 `command` 为空时可以返回空输出，但不得隐式创建新命令、隐式分配新 `panel` 或隐式恢复旧链路。
- 一个 `panel` 在任意时刻只能服务一条命令链路；处于活跃交互中的 `panel` 不得被并发分配给第二条普通命令。
- 复用空闲 `panel` 之前，tool 必须保证该 `panel` 已回到干净、可预测的 shell 基线；若无法确认旧前台进程、旧工作目录、旧环境变量或旧 shell 状态不会泄漏，则不得复用，必须销毁并重建新的 `window/panel`。
- 同一条交互链路内部，工作目录、环境变量和前台进程状态必须在其绑定的 `window/panel` 中持续可见；不同链路之间不得因为池化复用而意外共享未清理状态。
- 首次调用、reset 后调用、以及底层 `tmux session` 意外丢失后的调用，都必须遵循统一初始化策略，禁止让 executor 落入“以为 session 存在但 backend 已失效”的不一致状态。
- `reset=true` 的既有语义必须保持兼容：会清理共享 `session`、丢弃旧 pending 状态、清空空闲 `panel` 队列，并在后续普通命令调用时重建干净的共享 session。
- `reset=true` 与 `is_input=true` 同时出现时，必须继续遵循当前稳定语义：旧交互链路被终止，不得在 reset 后继续续写旧前台进程。
- tool 内部用于识别“活跃交互链路”的状态，必须与共享 `tmux session`、对应 `window/panel` 生命周期一致；当 `reset=true`、session 丢失、panel 被销毁或命令完成时，相关 pending 状态必须同步清理。
- 当前运行环境仍由上层提供，tool 层不得分支判断宿主机、Docker 或 remote。
- `TerminalObservation` 的基础语义必须保持兼容，至少包括 `exit_code`、`timeout`、`metadata.pid`、`metadata.working_dir`。
- 如需新增 `tmux`、pool、`window`、`panel` 或 pending 相关元数据，只能追加到 `metadata`，不得破坏现有字段兼容性。
- 若当前运行环境缺少 `tmux` 可执行文件，必须返回稳定错误，禁止 panic。
- 失败路径必须继续返回稳定 observation error，禁止 panic。
- 若 `panel` 分配失败、输入发送失败、屏幕读取失败、中断失败、命令完成标记解析失败、队列回收失败或 session 重建失败，必须返回稳定错误，并保证 pool 状态、pending 状态和 `window/panel` 状态可判定，禁止进入“旧交互链路已失效但仍可续写”或“panel 已不可用但仍留在空闲队列”的悬空状态。
- 当前基础审计日志必须保持可用，且至少能定位共享 session、目标 `window/panel` 和池化回收动作。
- 导出元素和文档注释必须使用中文，并符合 GoDoc 规范。
- 新增或删除 terminal 目录文件后，必须同步更新 `context.md`。

## Definition of Done

`4E` 算完成，当且仅当以下条件同时满足：

- 同一 tool 实例在 `reset=false` 的连续普通命令调用下，始终复用同一个共享 `tmux session`。
- 共享 `tmux session` 内可以按需创建多个 `window`，且每个 `window` 只承载一个可执行 `panel`。
- 普通命令执行完成后，其对应 `panel` 不会被立即销毁，而是进入空闲队列等待后续复用。
- 空闲 `panel` 队列默认长度为 `5`，当归还第 `6` 个空闲 `panel` 时，会稳定回收最旧空闲项而不是无限堆积。
- 复用出来的空闲 `panel` 可以继续承载新的普通命令，且不会泄漏上一条命令留下的前台进程、工作目录、环境变量或其他不可预测 shell 状态。
- 至少存在一条需要后续输入的命令链路：普通命令首次启动后，后续 `is_input=true` 且 `command` 非空的调用能够把输入送到同一个 `window/panel` 的前台进程。
- 至少存在一次 `is_input=true` 且 `command=""` 的调用，能够在不发送新输入的前提下读到同一 `window/panel` 的新增输出。
- 至少存在一次 `is_input=true` 且 `command="C-c"` 或等价中断指令的调用，能够中断同一 `window/panel` 的前台进程，而不是把文本写入 shell。
- 交互命令完成或被中断后，tool 内部 pending 状态会被清理；若 `panel` 满足复用条件，则可回收到空闲队列，否则会被销毁并从池中移除。
- 当不存在活跃交互链路时，`is_input=true` 的错误或空返回语义保持稳定且可预测。
- `reset=true` 后共享 session、活跃链路和空闲队列都会被清理；后续 `is_input=true` 不会继续写入 reset 前的交互链路。
- 底层共享 `tmux session` 意外丢失后，后续普通命令调用能够按统一策略恢复，不会留下错误的 pool 状态。
- `working_dir` 作为 session 或 `panel` 初始化参数仍然有效，且不会因池化复用路径被绕过。
- `TerminalObservation` 返回结构保持兼容。
- 本阶段未引入 `PTY` 依赖，也未要求上层显式感知 `session/window/panel` 标识。
- 已补齐本阶段基础测试。
- `context.md` 已更新为最新真实状态。

如果普通命令虽然复用了共享 `session`，但 `panel` 复用后仍泄漏旧状态，或者 `is_input=true` 不能稳定绑定到原始 `window/panel`，或者空闲队列不会回收溢出项，则不算完成。

## Test Contract

- 单元测试必须优先通过 mock `tmux` 适配层或命令执行层完成，禁止把真实 `tmux` 依赖作为单元测试前提。
- 如需补充真实 `tmux` 的集成或 live 测试，必须默认跳过，并通过显式环境变量开启。

- 共享 session 基线测试：
- 连续执行多条普通命令且 `reset=false` 时，所有命令都挂在同一个共享 `tmux session` 下。
- 至少验证一次 session 丢失后的重建路径，确保不会错误复用失效 session。

- `window/panel` 分配测试：
- 无空闲 `panel` 时，普通命令会在共享 session 下创建新的 `window/panel`。
- 存在空闲 `panel` 时，普通命令优先复用空闲队列，而不是继续无限创建新 `window/panel`。
- 同一个活跃 `panel` 不会被并发分配给第二条命令链路。

- 空闲队列测试：
- 命令完成后，可复用 `panel` 会进入空闲队列，而不是立即销毁。
- 空闲队列默认长度为 `5`。
- 当空闲队列溢出时，会稳定回收最旧空闲 `panel`，并销毁其对应 `window/panel` 资源。

- 隔离与复用测试：
- 被复用的 `panel` 在新命令启动前已回到干净 shell 基线，不会残留旧前台进程。
- 被复用的 `panel` 不会泄漏上一条命令留下的工作目录、环境变量或不可预测 shell 状态。
- 若某个空闲 `panel` 无法确认处于可复用状态，系统会销毁并补建，而不是带病复用。

- 输入续写测试：
- 首次调用启动需要后续输入的命令后，`is_input=true` 且 `command` 非空的调用能够把输入写入同一个 `window/panel`。
- 续写输入不得触发新 shell、新 `window` 或新共享 session 创建。
- 已完成的命令链路不得再被后续 `is_input=true` 误续写。

- 空输入拉输出测试：
- `is_input=true` 且 `command=""` 时不会因空命令直接报错。
- 不发送新输入也能读取当前绑定 `window/panel` 的新增输出。
- 连续多次空输入拉输出时，不会重复返回已经消费过的旧输出。

- 中断映射测试：
- 存在活跃前台进程时，`is_input=true` 且 `command="C-c"` 会调用目标 `window/panel` 的中断能力，而不是发送字面文本。
- 中断后能收到稳定的退出结果或中断结果，且 pending 状态被清理。
- 没有活跃前台进程时，中断返回稳定错误或稳定空结果，语义必须固定。

- reset 与 pool 协同测试：
- 已存在活跃交互链路和空闲队列时，`reset=true` 会清理共享 session、旧 pending 状态和空闲队列。
- reset 后再次 `is_input=true` 不会继续写入 reset 前的交互链路。
- reset 后若要继续交互，必须先重新启动新的命令链路。

- 当前执行参数测试：
- 初始 `working_dir` 能正确传递到共享 session 初始化路径以及新分配 `panel` 的可执行环境。
- 复用空闲 `panel` 后，新的 `working_dir` 语义仍然稳定，不会被旧状态绕过。
- 非法 `working_dir` 返回稳定错误。

- 兼容性测试：
- 普通命令执行路径继续可用。
- `TerminalObservation` 的 `exit_code`、`timeout`、`metadata` 字段行为保持兼容。
- `newBashTool()` 仍能正常注册。
- 缺失 `tmux` 依赖时返回稳定错误。

- 超时与错误测试：
- 共享 session 池化模式下超时语义仍然正确。
- `panel` 分配失败、回收失败、写入失败、拉输出失败、中断失败、命令结果解析失败时返回稳定错误 observation。
- 错误发生后不会留下不可判定的 pending 状态，也不会把失效 `panel` 留在空闲队列中。
