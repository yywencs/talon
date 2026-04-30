# terminal session-scoped runtime reuse SPEC

## Scope

- 本文件只约束 terminal 当前下一步改造：把默认 `bash` 路径从“每次调用临时创建 executor/backend/sandbox”收敛为“按 `session.id` 复用会话级 executor/backend/runtime/sandbox”。
- 当前步骤目标是：在保持 terminal 现有 `pane_id` 固定绑定语义不变的前提下，让同一逻辑会话内的多次 `bash` 调用稳定复用同一个 `Executor`、同一个 `TmuxBackend` 和同一个 Docker sandbox 容器。
- 当前步骤只解决以下问题：
  - `bash` 默认 Docker 路径的会话级复用主键与生命周期归属
  - executor/backend/runtime/sandbox 的创建、缓存、失效淘汰与释放
  - sandbox 初始化失败、依赖缺失或 backend 损坏时的稳定清理语义
  - 默认 sandbox 镜像与 `TmuxBackend` 对 `tmux` / `bash` 依赖之间的一致性
- 当前步骤不要求实现 remote runtime、agent-server、容器池预热、复杂失败恢复、命令拦截或更细粒度权限系统。

## Constraints

- 工具名必须继续是 `bash`。
- action 输入字段继续使用 `command`、`pane_id`、`is_input`、`timeout`、`reset`；`working_dir` 不得回流到 action。
- 当前 terminal 已有的固定 `pane_id -> tmux pane` 绑定语义必须保持不变；引入会话级复用后，不得退化为“一条命令一个新 shell”或“一次调用一个新 backend”。
- `session.id` 必须作为 `bash` 默认 Docker 路径的会话级复用主键；不得仅依赖固定容器名、固定 tmux session 名或工作目录字符串来伪装复用。
- 同一 `session.id` 下的连续 `bash` 调用，必须复用同一个 `Executor`；该 `Executor` 必须继续复用同一个 `TerminalBackend`、同一个 `Runtime` 和同一个 `Sandbox`。
- 不同 `session.id` 之间必须隔离；不同会话不得共享同一个 `Executor`、同一个 `TmuxBackend` 或同一个 Docker 容器。
- `pane_id` 仍然表示同一会话内的逻辑终端链路标识；`session.id` 负责会话级 executor/backend/sandbox 复用，二者职责不得混淆。
- 宿主机 runtime 仍保留为显式非默认路径；sandbox 路径失败时不得静默回退到宿主机 runtime。
- `TmuxBackend.Initialize()` 若因 sandbox 镜像缺少 `tmux`、缺少 `bash`、sandbox 启动失败或其他初始化错误而失败，必须清理当前已创建但不可用的 backend/runtime/sandbox，并从会话级缓存中移除失效实例。
- 同一 `session.id` 的首次创建若失败，下次调用允许重新创建；不得把已失败实例永久留在缓存中。
- 会话级缓存必须有明确释放语义；当会话结束、显式关闭或实现定义的空闲清理触发时，必须关闭 backend 并进一步释放 runtime/sandbox 资源。
- `TerminalObservation` 的基础语义必须保持兼容，至少包括 `exit_code`、`timeout`、`metadata.pid`、`metadata.working_dir`。
- 当前基础审计必须保持可用；至少应能区分 `session.id`、逻辑 `pane_id`、当前 runtime 路径、sandbox/container 标识以及初始化失败/实例淘汰/释放等关键动作。
- 默认 sandbox 镜像必须与 `TmuxBackend` 依赖保持一致；当默认路径要求在 sandbox 内运行 `tmux` 时，镜像中必须稳定提供 `tmux` 与 `bash` 或等价依赖，不得依赖运行时临时安装。
- 导出元素和文档注释必须使用中文，并符合 GoDoc 规范。
- 更新 terminal 目录文件或行为契约后，必须同步更新 `context.md`。

## Definition of Done

- 同一 `session.id` 下连续多次默认 `bash` 调用时，只会创建一次会话级 `Executor`。
- 同一 `session.id` 下连续多次默认 `bash` 调用时，只会创建一次 `TmuxBackend` 和一次 Docker sandbox 容器，后续调用复用既有实例。
- 不同 `session.id` 的调用会得到彼此隔离的 executor/backend/sandbox，不会串用 shell 状态、pane 绑定或容器实例。
- 同一会话内现有 `pane_id` 固定绑定语义保持稳定；复用 executor/backend 后，跨命令上下文、工作目录和输入续写语义不发生回退。
- 当 sandbox 初始化失败、镜像缺少 `tmux` / `bash`、backend 初始化失败或实例损坏时，系统会稳定返回错误，并清理当前失败实例，不留下悬空缓存或无用容器。
- 会话结束或显式释放后，会话级 executor/backend/runtime/sandbox 会被关闭并从缓存中移除。
- `context.md` 与 `spec.md` 已反映最新真实状态，不再描述“terminal 尚未接入 Docker runtime”的旧事实。

## Test Contract

- 当前步骤测试以单元测试为主，优先通过 mock executor/backend/sandbox runner 完成；禁止把真实 Docker、真实 tmux 或真实镜像拉取作为单元测试前提。
- 如需补充真实 Docker / tmux 集成测试，必须默认跳过，并通过显式环境变量开启。
- 至少覆盖以下场景：
  - 同一 `session.id` 连续调用默认 `bash` 路径时，只创建一次 executor/backend/sandbox
  - 不同 `session.id` 会创建彼此隔离的 executor/backend/sandbox
  - 同一 `session.id` 下多个不同 `pane_id` 仍能复用同一个 executor/backend，并保持原有固定 pane 语义
  - sandbox 初始化失败后，失败实例会从缓存移除，并执行清理逻辑
  - 下一次同一 `session.id` 调用在前一次失败后可以重新创建新实例
  - 显式释放或会话结束后，缓存实例会被关闭并移除
  - sandbox 路径失败时不会静默回退到宿主机 runtime
- 禁止为了覆盖率编写低价值样板测试；优先验证复用边界、隔离边界、失败清理和释放语义。
