# terminal 4B 单 tmux session SPEC

## Scope

- 本文件只约束 `4B` 单 `tmux session`。
- `4B` 目标是以 `tmux` 作为终端会话载体，在当前运行环境中承接已有持久化 shell 能力。
- `4B` 要打通三件事：连续命令复用同一会话、`is_input=true` 的输入续写、空输入拉输出。
- `4B` 仍保持“上层决定运行环境，tool 只在当前运行环境执行”这一边界。
- `4B` 不要求在 tool 层区分宿主机、Docker 或 remote。
- `4B` 不要求实现池化 `tmux`。
- `4B` 不要求真正实现 `reset=true`。
- `4B` 不要求引入完整 sandbox policy。
- `4B` 不要求修改 `TerminalObservation` 的基础返回结构。

## Constraints

- 工具名必须继续是 `bash`。
- action 输入字段继续保持 `command`、`is_input`、`timeout`、`reset` 四个字段。
- `working_dir` 必须继续作为当前运行环境参数或 session 初始化参数存在，不得回流到 action。
- session 必须由 tool 层持有或可访问，但不得要求上层 action 显式传 `session_id`。
- 同一 tool 实例上的连续调用必须优先复用同一个 `tmux session`，不得退回到“一条命令启动一个新 shell”的语义。
- `tmux` 必须成为会话主载体；`PTY` 不再作为本阶段实现前提。
- 同一 `tmux session` 中的工作目录、环境变量和前台进程状态必须在后续调用中可见。
- 当 `is_input=false` 且 `reset=false` 时，普通命令必须写入当前 `tmux session` 并返回对应输出。
- 当 `is_input=true` 且 `command` 非空时，必须将 `command` 作为续写输入发送到当前 `tmux session`，而不是新开 shell 执行。
- 当 `is_input=true` 且 `command` 为空时，必须允许“空输入拉输出”，即不发送新输入也能读取当前会话新增输出。
- `4B` 必须具备基础会话生命周期：首次调用可创建会话，后续调用可复用会话；底层 `tmux session` 意外丢失时，必须返回稳定错误或按统一策略重建，禁止进入不一致状态。
- `reset=true` 在 `4B` 中允许继续返回稳定未实现错误，但不得影响非 `reset` 路径。
- 当前运行环境仍由上层提供，tool 层不得分支判断宿主机、Docker 或 remote。
- `TerminalObservation` 的基础语义必须保持兼容，至少包括 `exit_code`、`timeout`、`metadata.pid`、`metadata.working_dir`。
- 如需新增 `tmux` 相关元数据，只能追加到 `metadata`，不得破坏现有字段兼容性。
- 若当前运行环境缺少 `tmux` 可执行文件，必须返回稳定错误，禁止 panic。
- 失败路径必须继续返回稳定 observation error，禁止 panic。
- 当前基础审计日志必须保持可用。
- 导出元素和文档注释必须使用中文，并符合 GoDoc 规范。
- 新增或删除 terminal 目录文件后，必须同步更新 `context.md`。

## Definition of Done

`4B` 算完成，当且仅当以下条件同时满足：

- 连续两次普通命令调用能够复用同一个 `tmux session`。
- 至少一次 `cd` 后再执行 `pwd` 时，第二条命令能看到前一条命令留下的工作目录状态。
- 至少一次 `export` 后再读取环境变量时，后续命令能看到同一会话中的环境变量变化。
- 至少存在一条需要续写输入的命令链路，后续一次 `is_input=true` 且 `command` 非空的调用能够把输入送到同一前台进程。
- 至少存在一次 `is_input=true` 且 `command=""` 的调用，能够在不发送新输入的前提下读到当前会话新增输出。
- `working_dir` 作为初始执行参数仍然有效。
- `TerminalObservation` 返回结构保持兼容。
- `reset=true` 在本阶段仍可返回稳定未实现错误，但不得影响普通命令路径和 `is_input` 路径。
- 本阶段未引入 `PTY` 依赖，也未引入池化 `tmux`。
- 已补齐本阶段基础测试。
- `context.md` 已更新为最新真实状态。

如果普通命令或续写输入仍然绕过 `tmux session`，导致无法保留工作目录、环境变量或前台进程状态，则不算完成。

## Test Contract

- 单元测试必须优先通过 mock `tmux` 适配层或命令执行层完成，禁止把真实 `tmux` 依赖作为单元测试前提。
- 如需补充真实 `tmux` 的集成或 live 测试，必须默认跳过，并通过显式环境变量开启。

- `tmux session` 复用测试：
  - 连续执行两条普通命令时，第二条命令能看到第一条命令留下的会话状态。
  - 至少验证一次工作目录在连续命令间保持一致。
  - 至少验证一次环境变量在连续命令间保持一致。

- 输入续写测试：
  - 首次调用启动需要后续输入的命令后，`is_input=true` 且 `command` 非空的调用能够把输入写入同一会话。
  - 续写输入不得触发新 shell 或新 `tmux session` 创建。

- 空输入拉输出测试：
  - `is_input=true` 且 `command=""` 时不会因空命令直接报错。
  - 不发送新输入也能读取当前会话新增输出。

- 当前执行参数测试：
  - 初始 `working_dir` 能正确传递到第一个 `tmux session` 对应 shell。
  - 非法 `working_dir` 返回稳定错误。

- 兼容性测试：
  - 普通命令执行路径继续可用。
  - `TerminalObservation` 的 `exit_code`、`timeout`、`metadata` 字段行为保持兼容。
  - `newBashTool()` 仍能正常注册。
  - 缺失 `tmux` 依赖时返回稳定错误。

- 未实现能力测试：
  - `reset=true` 时继续返回稳定未实现错误。
  - 池化 `tmux` 相关能力在本阶段不暴露新接口、不提前落地。

- 超时与错误测试：
  - `tmux session` 复用模式下超时语义仍然正确。
  - `tmux session` 创建失败、写入失败、拉输出失败时返回稳定错误 observation。
