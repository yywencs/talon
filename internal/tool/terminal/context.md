# terminal context

## Current State

- `internal/tool/terminal` 已完成 terminal 主语义收敛：当前 action 输入稳定为 `command`、`pane_id`、`is_input`、`timeout`、`reset`，执行编排、observation、错误语义、审计与基础测试都已进入该目录。
- 当前 terminal 后端已经收敛为统一 `TerminalBackend` 接口；`TmuxBackend` 已完成“共享 `tmux session` + `pane_id` 长期固定绑定同一个 pane”的模型，普通命令、续写输入、空输入拉输出、`C-c` 中断与 `reset=true` 都围绕同一个逻辑 `pane_id` 工作。
- 当前 `Executor` 内部已使用 `map[string]*pendingExecution` 和每 `pane_id` 独立锁来支持多条逻辑终端链路并发；同一个 `pane_id` 的上下文、工作目录和环境变量可跨命令保留。
- 当前 terminal 已接入 Docker sandbox runtime：`internal/tool/terminal_tool.go` 的默认 `bash` 路径会创建绑定 sandbox runtime 的 `TmuxBackend`，宿主机 runtime 仅保留为显式非默认路径。
- 当前 sandbox runtime 会在 `TmuxBackend.Initialize()` 阶段先 `Prepare()` 启动 Docker 容器，再在容器内检查 `tmux` 与 `bash`；因此默认 sandbox 镜像若缺少这些依赖，terminal 初始化会直接失败。
- 当前默认 `bash` 路径已改为按 `session.id` 获取或创建会话级 executor：同一逻辑会话内会复用同一个 `Executor`、同一个 `TmuxBackend`、同一个 `Runtime` 与同一个 Docker sandbox 容器，不同 `session.id` 之间保持隔离。
- 当前 `session.id` 通过工具执行 `context` 透传到 `bash` 工具；默认 sandbox 路径缺少 `session.id` 时会稳定报错，不再退化为“临时新建 executor”的伪复用。
- 当前默认 `bash` 路径已补上初始化失败清理：当 `TmuxBackend.Initialize()` 或其依赖的 sandbox/runtime 初始化失败时，会关闭 backend 并把失效实例从会话级缓存移除，下一次同一 `session.id` 调用可重新创建。
- 当前 `core.Session.Run()` 在会话结束或运行异常退出时会释放对应 `session.id` 的 bash 会话级实例；暂停/卡住场景暂不主动释放，以保留后续续跑能力。
- 当前 terminal 会话级复用已补充基础日志：`bash` 调用开始/结束、executor 创建/复用/失效清理/释放等关键点都能稳定关联 `session.id`、runtime 路径以及 sandbox 镜像/容器标识。
- 当前默认 sandbox 路径中原先 `sandboxTmuxBackend` 的能力透传缺口已彻底收敛：`NewSandboxTmuxBackend()` 不再返回额外 wrapper，而是直接返回绑定 sandbox runtime 的真实 `TmuxBackend`，`PrepareCommand`、`CompleteCommand`、`InvalidateCommand`、`ResetPane`、`PanePID` 和 `CurrentWorkingDir` 由核心 backend 直接提供。
- 当前 terminal 内部真正依赖的 backend 能力已收敛为统一的命名接口：基础 `TerminalBackend`、`TerminalBackendCommandLifecycle`、`TerminalBackendMetadata` 和组合能力面 `TerminalBackendCapabilities` 已在 `internal/tool/terminal/backend.go` 中集中定义。
- 当前 `Executor` 与默认 sandbox 路径已经复用同一套能力接口与辅助函数，不再各自散落匿名接口类型断言；重复断言和重复转发逻辑已明显减少。
- 当前 session 级实例失效摘除已从额外 backend 代理层收回到 `Executor` 与 `bashSessionRegistry` 的协作中：`Executor` 在 backend 返回错误时通过回调上报，registry 负责摘除并关闭失效实例，`managedBashBackend` 这一层职责混合代理已被移除。
- 当前 `bashSessionRegistry` 已进一步收敛为“缓存 / 复用约束校验 / 失效摘除 / 释放”职责；它组装审计字段时不再通过 backend 类型断言反查 `SandboxInfo()`，而是直接使用 session entry 持有的 sandbox info provider。
- 当前默认 sandbox 路径的装配已拆成 `backend + sandbox info provider` 两条并行链路：backend 继续只承担 terminal 会话能力，sandbox 审计信息则由装配阶段单独注入给 session entry。
- 当前 `SandboxInfo()` 归属与 terminal backend 能力面继续保持解耦；默认 sandbox 路径的装配已经收敛为“session entry 持有 sandbox info provider + backend 直接使用真实 `TmuxBackend`”。
- 当前 `internal/sandbox/terminal_runtime.go` 中用于包装 backend 的 `sandboxTmuxBackend` 已被移除；默认 sandbox 路径不再通过额外 backend 壳层传递 lifecycle / metadata 能力。
- 当前 host / sandbox 路由差异已从 `bashSessionRegistry` 的核心逻辑中继续下沉：`execution profile` 这层中间抽象已被移除，registry 只接收最小必要参数，如 runtime 路径标签、工作目录和 backend bundle 创建函数。
- 当前 `terminal_tool.go` 中共享执行链里的 `bashRuntimeRoute` 枚举与显式 switch 已被移除；默认 `bash` 路径与显式 host 路径现在分别通过两个薄构造入口装配 executor，route 细节不再向下传递到共享执行链。
- 当前按阶段制推进的收敛路线中，前四个阶段已经完成：修复 wrapper 能力缺口、统一 backend 能力接口、拆回 session 失效摘除职责、解耦 `SandboxInfo()` 归属。
- 当前 `Phase E` 已完成：默认 sandbox 路径的多余 backend wrapper 已被移除；`Phase F` 已完成前三轮收敛，把 route 分叉从 registry 核心路径中拿掉，压掉 `execution profile` 中间层，并进一步移除了共享执行链里的 `bashRuntimeRoute` 枚举与显式 switch，当前差异主要保留在默认 sandbox / 显式 host 两个薄构造入口。

## Roadmap

- `Phase A` 已完成：修复 `sandboxTmuxBackend` 的 lifecycle / metadata 能力透传缺口，恢复默认 sandbox 路径下的 `pane_id -> tmux pane` 绑定链路。
- `Phase B` 已完成：把 terminal 内部真正依赖的 backend 能力收敛成统一接口，减少 `Executor`、session registry 和 sandbox wrapper 中散落的类型断言。
- `Phase C` 已完成：把 session 级实例失效摘除职责收回 `Executor + bashSessionRegistry` 协作链路，移除 `managedBashBackend` 这种职责混合代理。
- `Phase D` 已完成：把 `SandboxInfo()` 从 backend 能力面中解耦，改为由装配阶段单独注入给 session entry，registry 不再通过 backend 类型断言读取审计信息。
- `Phase E` 已完成：继续减少 `sandboxTmuxBackend` 这类代理层，收敛 `internal/sandbox/terminal_runtime.go` 的装配方式；默认 sandbox 路径已经不再为了能力适配保留多余 backend 包装。
- `Phase F` 已完成：把 host / sandbox 差异进一步收敛到 runtime、runner 或构造阶段，保持当前默认 tmux 路径稳定，同时避免把终端核心抽象永久绑定到 tmux，为后续引入 PTY 预留实现空间。当前前三轮已完成：route 分叉已从 registry 核心路径拿掉，`execution profile` 中间层已压掉，共享执行链里的 `bashRuntimeRoute` 枚举与显式 switch 也已移除，差异主要收敛到默认 sandbox / 显式 host 两个薄构造入口。
- `Phase G` 已完成大半：补齐 session 主动释放、默认 sandbox 镜像依赖对齐，以及同 session 复用、跨 session 隔离、失败清理和 session 结束回收测试。
- 最终目标：让 `session registry` 只负责实例缓存、复用约束、失效摘除和释放；让 backend 只负责终端能力；让 host / sandbox 差异不再通过多层 backend wrapper 传递。

## Constraints

- `context.md` 只记录当前真实状态和明确下一步，不在这里提前展开理想化架构。
- terminal 层只负责复用“当前运行环境”的 executor/backend/runtime，不应直接下沉 Docker 细节、镜像安装逻辑或复杂权限策略。
- 同一个逻辑会话一旦绑定某个 terminal executor / runtime / sandbox，就不应在同一会话生命周期内中途切换到其他 runtime 或其他容器。
- `session.id` 应作为 terminal 会话级复用的主键；不得仅通过固定容器名字符串来伪装复用。
- sandbox 初始化失败时不得静默回退到宿主机 runtime；失败语义必须稳定且可清理。
- 保持当前 `pane_id` 语义不变；如果上层已显式传入 `pane_id`，工具层不得再覆盖为固定默认值。
- 当前阶段按 `Phase A -> Phase G` 推进；每完成一个阶段就标记完成，不再反复重开号或重新定义新的“第一步”。
- 在内部 wrapper 尚未收敛前，新增 backend 包装层不得裁掉现有生命周期接口或 metadata 接口能力。
- terminal 内部对 backend 可选能力的判断与适配应优先复用 `internal/tool/terminal/backend.go` 中集中定义的统一接口和辅助函数，不再新增散落匿名接口。
- session registry 只负责实例缓存、复用约束校验、失效摘除和释放；backend 失效检测若需要上报，应优先通过 executor 回调等明确链路完成，而不是再引入职责混合代理层。
- `SandboxInfo()` 等附加信息应继续保持与 backend 能力面解耦；后续若继续减少代理层，也不应再把审计信息重新挂回 backend 包装层。
- 完整重构方向必须持续收敛到“session registry 与 backend 代理职责拆干净”，避免在阶段推进过程中重新引入新的混合职责包装层。
- 默认 sandbox 路径应继续直接复用真实 `TmuxBackend` 能力；后续如需表达 host / sandbox 差异，优先在 runtime、runner 或构造阶段处理，而不是重新加回 backend wrapper。
- 当前 `Phase F` 中 route 细节应继续保持“只停留在两个薄构造入口”的边界；registry、共享执行链和 backend 能力面不应重新接收或扩散 host / sandbox 分叉逻辑，也不应重新引入新的中间配置壳层。
- 当前阶段不同时展开 remote runtime、agent-server、多种 sandbox 池化或命令策略引擎。
- 所有改造都应优先可测试，避免把 session 注册表、executor 生命周期、backend 状态与 Docker 清理逻辑耦合在一个巨大结构体中。

## Code Anchor

- `internal/tool/terminal_tool.go`：当前 `bash` 工具兼容入口；默认 sandbox 路径下 `backend + sandbox info provider` 的装配入口。
- `internal/tool/terminal_tool.go`：当前 `bash` 工具兼容入口；默认 sandbox / 显式 host 两个薄构造入口与 backend bundle 的收敛入口。
- `internal/tool/terminal/execute.go`：terminal 主执行编排、pending 生命周期、`PrepareCommand` / `CompleteCommand` 调用位置，以及 backend 失败回调上报链路。
- `internal/tool/terminal/backend.go`：统一 `TerminalBackend` 能力面、生命周期接口、metadata 接口及其辅助获取函数。
- `internal/tool/terminal/tmux_backend.go`：`TmuxBackend.Initialize()`、runner `Prepare()`、`tmux`/`bash` 依赖检查与 session 生命周期。
- `internal/tool/bash_session_registry.go`：会话级 executor/backend 缓存、复用约束校验、失效摘除、释放，以及 sandbox 审计字段组装入口；当前只消费最小必要构造参数，不再持有 route 配置对象。
- `internal/sandbox/runtime.go`：host runtime / sandbox runtime 统一抽象，以及 sandbox runtime 的 `Prepare()` / `Close()` 行为。
- `internal/sandbox/terminal_runtime.go`：terminal 与 sandbox runtime 的装配入口；当前默认 sandbox 路径已直接返回绑定 runtime 的真实 `TmuxBackend`。
- `internal/sandbox/docker.go`：Docker sandbox 的启动、关闭、执行和容器参数组装。
- `internal/sandbox/sandbox.go`：默认镜像、工作目录和 sandbox 基础配置。
- `internal/core/session.go`：会话主循环与 `session.id` 来源；后续 terminal 会话级复用需要围绕这里的会话生命周期接线。
- `internal/types/state.go`：`SessionState.ID` 定义，是 terminal/sandbox 会话级复用的稳定主键来源。
