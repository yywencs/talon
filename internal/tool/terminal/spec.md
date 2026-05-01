# terminal runtime convergence phase-f SPEC

## Scope

- 本文件只约束 terminal 当前 `Phase F` 改造：把 host / sandbox 差异进一步收敛到 runtime、runner 或构造阶段，保持当前默认 tmux 路径稳定，同时避免把 terminal 核心抽象永久绑定到 tmux，为后续引入 PTY 预留实现空间。
- 当前步骤目标是：在保持 terminal 现有 `pane_id` 固定绑定语义、session 级 executor 复用语义和默认 Docker sandbox 路径不变的前提下，让 host / sandbox 差异更多体现在 runtime 来源、构造参数和装配阶段，而不是继续外溢到上层调用链。
- 当前步骤只解决以下问题：
  - 收敛 host / sandbox 路由在 backend 构造过程中的分叉点，减少上层对 route 细节的感知
  - 让默认 tmux 路径继续作为当前稳定实现，但把差异更多地下沉到 runtime、runner 或构造参数层
  - 在 `Phase F` 首轮已经把 route 分叉从 registry 核心路径中拿掉的基础上，继续压掉 `execution profile` 这层中间抽象，让 route 选择只停留在更薄的工具构造入口
  - 在 `Phase F` 前两轮已经把 route 分叉压到工具构造入口的基础上，继续移除共享执行链里的 `bashRuntimeRoute` 枚举与显式 switch，让默认 sandbox / 显式 host 只保留两个薄构造入口
  - 在继续使用 `TmuxBackend` 作为当前默认实现的同时，避免把 terminal 核心语义设计成永久等同于 tmux 语义
  - 补充围绕 runtime 收敛和默认 sandbox / host 路径稳定性的最小回归测试
- 当前步骤不要求直接实现 `PTYBackend`、重写 `TmuxBackend` 核心执行模型、调整默认镜像策略，或同时展开 remote runtime / agent-server 等远期方向。

## Constraints

- 工具名必须继续是 `bash`。
- action 输入字段继续使用 `command`、`pane_id`、`is_input`、`timeout`、`reset`；`working_dir` 不得回流到 action。
- 当前 terminal 已有的固定 `pane_id -> tmux pane` 绑定语义必须保持不变；引入 runtime 收敛后，不得退化为“一条命令一个新 shell”或“一次调用一个新 backend”。
- `session.id` 必须继续作为 `bash` 默认 Docker 路径的会话级复用主键；不得仅依赖固定容器名、固定 tmux session 名或工作目录字符串来伪装复用。
- 同一 `session.id` 下的连续 `bash` 调用，必须继续复用同一个 `Executor`；该 `Executor` 必须继续复用同一个 backend、同一个 `Runtime` 和同一个 `Sandbox`。
- 不同 `session.id` 之间必须隔离；不同会话不得共享同一个 `Executor`、同一个 `TmuxBackend` 或同一个 Docker 容器。
- `pane_id` 仍然表示同一会话内的逻辑终端链路标识；`session.id` 负责会话级 executor/backend/sandbox 复用，二者职责不得混淆。
- terminal 内部已经统一的 backend 能力接口必须继续复用；本步骤不得回退到新增散落匿名接口或重复断言。
- `SandboxInfo()` 等附加信息必须继续与 backend 能力面解耦；本步骤不得为了减少 wrapper 层而把审计信息重新挂回 backend 包装层。
- session registry 必须继续只负责实例缓存、复用约束校验、失效摘除、释放和审计字段组装；不得重新承担 backend 能力适配职责。
- host / sandbox 差异应优先收敛到 runtime 选择、runner 装配或 backend 构造参数层；不得继续把 route 细节散落到 executor、registry 或上层调用路径中。
- `Phase F` 首轮引入的 `execution profile` 只允许作为临时过渡形态；本轮应尽量将其移除或压缩为更薄的构造参数传递，不再保留新的中间配置对象。
- 在 `Phase F` 下一轮中，tool 层共享执行链不应继续传递 `bashRuntimeRoute` 这类 route 枚举；默认 sandbox / 显式 host 的差异应收敛为不同的构造函数、bundle factory 或等价的薄入口。
- 当前默认实现仍可继续使用 `TmuxBackend`，但 terminal 核心语义和阶段性文档不得把 tmux 写死为永久唯一实现，以免阻断后续引入 PTY 的演进空间。
- 新增或调整的构造层不得再次裁掉 `TmuxBackend` 已有的 lifecycle 或 metadata 能力；若继续通过构造入口表达差异，其能力面必须与统一 backend 接口保持一致。
- 当前步骤应优先减少 route 感知和装配分叉，但不得把 terminal 包显式改造成感知 Docker 细节的实现。
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

- host / sandbox 路由差异在构造链路中的暴露面已明显减少；上层主要依赖统一 backend 能力和稳定装配入口，而不是关心具体 route 细节。
- `execution profile` 这层中间抽象已被移除或显著压薄；工具构造入口直接决定最小必要参数，registry 不重新理解 route 语义，也不新增新的配置壳层。
- tool 层共享执行链里的 `bashRuntimeRoute` 枚举与显式 switch 已被移除或明显压薄；默认 sandbox / 显式 host 路径通过更薄的构造入口装配 executor，而不是把 route 标识继续往下传。
- 默认 tmux 路径继续稳定工作；默认 sandbox / host 路径下，`PrepareCommand`、`ReadScreen`、`PanePID` 和 `CurrentWorkingDir` 等行为不因 runtime 收敛而回归到未绑定 pane、metadata 丢失或能力不可见。
- backend 能力面与附加审计信息能力继续保持拆开；route 差异不再通过新增 backend wrapper 或额外上层类型分叉来表达。
- 同一 `session.id`、同一 `pane_id` 下的既有交互语义保持不变；本次收敛后，不会回退为新 shell、临时 pane 或宿主机 fallback。
- 现有 session 级复用、跨 session 隔离、失败清理和关闭语义不因本次 runtime 收敛而被破坏。
- `context.md` 与 `spec.md` 已对齐，能明确表达当前步骤是 `Phase F` 的 runtime / 构造收敛，同时保留未来引入 PTY 的演进空间。

## Test Contract

- 当前步骤测试以单元测试为主，优先通过 mock backend / runtime / sandbox runner 完成；禁止把真实 Docker、真实 tmux 或真实镜像拉取作为单元测试前提。
- 如需补充真实 Docker / tmux 集成测试，必须默认跳过，并通过显式环境变量开启。
- 至少覆盖以下场景：
  - host / sandbox 路由差异在构造链路中已收敛，但默认 sandbox 路径下 `PrepareCommand` 仍会命中真实 backend 并建立固定 `pane_id` 绑定
  - 默认 sandbox / host 路径下，`PanePID` 和 `CurrentWorkingDir` 等 metadata 能力不会因 runtime 收敛而丢失
  - 移除 `execution profile` 后，registry 仍只依赖最小必要参数完成复用、失效摘除和审计字段组装
  - 移除 tool 层共享执行链中的 route 枚举后，默认 sandbox / 显式 host 两条路径仍分别保持会话复用与非复用语义
  - backend 初始化失败或初始化后失效时，session registry 仍会摘除并关闭对应实例
  - 现有 session 级复用、跨 session 隔离和失败清理相关测试不会因本次 runtime / 构造收敛回归
- 禁止为了覆盖率编写低价值样板测试；优先验证 route 差异是否真正下沉到更薄的 runtime / 构造层，同时保持固定 pane 绑定和 metadata 语义稳定。
