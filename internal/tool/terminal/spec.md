# terminal 4A 持久化 subprocess SPEC

## Scope

- 本文件只约束 `4A` 持久化 subprocess。
- `4A` 目标是让连续普通命令复用同一个长期存活的 shell subprocess。
- `4A` 仍保持“上层决定运行环境，tool 只在当前运行环境执行”这一边界。
- `4A` 不要求在 tool 层区分宿主机、Docker 或 remote。
- `4A` 不要求引入 PTY。
- `4A` 不要求真正实现 `is_input`。
- `4A` 不要求真正实现 `reset`。
- `4A` 不要求引入完整 sandbox policy。
- `4A` 不要求修改 `TerminalObservation` 的基础返回结构。

## Constraints

- 工具名必须继续是 `bash`。
- action 输入字段继续保持 `command`、`is_input`、`timeout`、`reset` 四个字段。
- `working_dir` 必须继续作为当前运行环境参数或 session 参数存在，不得回流到 action。
- `4A` 只要求支持普通命令模式，即 `is_input=false` 且 `reset=false` 时的连续命令复用。
- session 必须由 tool 层持有或可访问，但不得要求上层 action 显式传 `session_id`。
- 同一 tool 实例上的连续普通命令调用必须作用于同一个 shell subprocess。
- 同一 shell subprocess 中的工作目录和环境变量变更必须在后续普通命令中可见。
- `is_input`、`reset` 在 `4A` 中允许继续返回稳定未实现错误。
- 当前运行环境仍由上层提供，tool 层不得分支判断宿主机、Docker 或 remote。
- `TerminalObservation` 的基础语义必须保持兼容，至少包括 `exit_code`、`timeout`、`metadata.pid`、`metadata.working_dir`。
- 失败路径必须继续返回稳定 observation error，禁止 panic。
- 当前基础审计日志必须保持可用。
- 导出元素和文档注释必须使用中文，并符合 GoDoc 规范。
- 新增或删除 terminal 目录文件后，必须同步更新 `context.md`。

## Definition of Done

`4A` 算完成，当且仅当以下条件同时满足：

- 连续两次普通命令调用能够复用同一个 shell subprocess。
- 至少一次 `cd` 后再执行 `pwd` 时，第二条命令能看到前一条命令留下的工作目录状态。
- 至少一次 `export` 后再读取环境变量时，后续命令能看到同一 shell 中的环境变量变化。
- `working_dir` 作为初始执行参数仍然有效。
- `TerminalObservation` 返回结构保持兼容。
- `is_input`、`reset` 在本阶段仍可返回稳定未实现错误，但不得影响普通命令路径。
- 已补齐本阶段基础测试。
- `context.md` 已更新为最新真实状态。

如果普通命令仍然是一条命令启动一个新 shell，无法保留前一条命令的工作目录或环境变量状态，则不算完成。

## Test Contract

- subprocess 复用测试：
  - 连续执行两条普通命令时，第二条命令能看到第一条命令留下的 shell 状态。
  - 至少验证一次工作目录在连续命令间保持一致。
  - 至少验证一次环境变量在连续命令间保持一致。

- 当前执行参数测试：
  - 初始 `working_dir` 能正确传递到第一个 shell subprocess。
  - 非法 `working_dir` 返回稳定错误。

- 兼容性测试：
  - 普通命令执行路径继续可用。
  - `TerminalObservation` 的 `exit_code`、`timeout`、`metadata` 字段行为保持兼容。
  - `newBashTool()` 仍能正常注册。

- 未实现能力测试：
  - `is_input=true` 时继续返回稳定未实现错误。
  - `reset=true` 时继续返回稳定未实现错误。

- 超时与错误测试：
  - subprocess 复用模式下超时语义仍然正确。
  - shell subprocess 启动失败时返回稳定错误 observation。
