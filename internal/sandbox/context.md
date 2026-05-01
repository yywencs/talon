# sandbox context
## 各文件作用

- `context.md`：记录 sandbox 当前真实状态、下一步实现顺序、约束和代码锚点。
- `spec.md`：约束 sandbox 当前阶段实现范围，防止 runtime 装配和 terminal 接线过度扩张。
- `sandbox.go`：定义 sandbox 最小核心抽象，包括状态、配置、实例接口和工厂接口。
- `manager.go`：提供面向上层装配层的最小创建入口，并封装默认占位工厂。
- `unimplemented.go`：提供当前阶段的 sandbox 占位实现，统一未实现生命周期行为。
- `errors.go`：定义 sandbox 阶段 1 需要暴露的稳定错误。
- `docker.go`：提供基于 Docker CLI runner 的最小真实 sandbox runtime，实现启动、关闭和容器内命令执行。
- `docker_policy.go`：定义 Docker sandbox 的最小目录边界、只读挂载、输出限制和稳定错误分类策略。
- `audit.go`：定义 sandbox 执行审计 span 的最小字段集合，并复用 `pkg/observability`。
- `runtime.go`：提供“当前运行环境”的统一抽象，收敛 host runtime 与 sandbox runtime 的最小执行能力。
- `terminal_runtime.go`：提供面向 terminal 的最小装配入口，显式创建绑定 host runtime 或 sandbox runtime 的 tmux backend。
- `sandbox_test.go`：覆盖阶段 1、阶段 2、阶段 3 与阶段 4 的契约测试，包括默认镜像、参数组装、runtime 绑定、目录边界、超时、输出截断与最小审计字段。

## 当前状态

- `internal/sandbox` 已完成阶段 1、阶段 2、阶段 3，并已落地阶段 4 的最小安全控制闭环：当前已经具备抽象接口、默认管理入口、最小 Docker runtime、统一 runtime 抽象、terminal 装配入口以及基础安全控制。
- 当前目标是让工具执行从“直接运行在宿主机”演进为“具备隔离、权限控制、审计与恢复能力的可控执行系统”。
- 当前 terminal 能力已经基本收拢到 `internal/tool/terminal`：命令执行、交互式输入、`reset`、超时、输出截断、固定 `pane_id` 绑定和 backend 抽象都已具备。
- 当前 terminal 后端本身仍保持 runtime 无感知；但默认工具入口已经切到 Docker sandbox，对应的 tmux backend 通过 runner 注入在沙箱内执行。
- 当前 `internal/sandbox/runtime.go` 已把宿主机与 sandbox 收敛成统一的最小执行能力；host runtime 与 sandbox runtime 都可以作为“当前运行环境”注入 terminal。
- 当前 `terminal_tool.go` 仍是兼容入口，`pane_id` 暂时可先固定，当前不需要先解决多 pane 调度问题才能开始 sandbox 基础建设。
- 当前 sandbox 已支持最小 Docker runtime：可以创建 Docker sandbox、按默认镜像 `golang:alpine` 启动容器、约定容器内工作目录并执行基础命令。
- 当前真实 runtime 仍保持收敛：基于可 mock 的 Docker CLI runner 实现，且 terminal 侧只消费统一 runner 能力，不暴露 Docker 显式语义。
- 当前已提供显式装配入口，可以创建绑定 host runtime 或 sandbox runtime 的 tmux backend；默认 `bash` 工具入口已切到 Docker sandbox，宿主机 runtime 仅保留为显式非默认路径，且 sandbox 准备失败时不会静默回退到宿主机。
- 当前 Docker sandbox 已补最小目录边界：workspace 目录可写、根文件系统只读、运行时临时目录通过 `tmpfs` 提供，额外宿主机路径只允许只读挂载。
- 当前已补最小执行保护：`Exec()` 支持统一超时兜底、聚合输出大小限制、稳定截断标记，以及超时/取消的稳定错误语义。
- 当前已补最小审计字段：sandbox 执行会通过 `pkg/observability` 写入 runtime、container、image、workspace、command、exit_code、timed_out、output_truncated、stdout_bytes、stderr_bytes 和 error_reason 等语义。
- 当前 `bash` 默认 sandbox 路径会把 session 级 executor 缓存在 `internal/tool/bash_session_registry.go`，以便同一个会话复用同一组 backend / runtime / sandbox。
- 当前这套 session 级缓存只在两类时机清理：一是 `managedBashBackend.Initialize()` 初始化失败时触发失效回调；二是 `internal/core/session.go` 在会话结束时显式调用 `ReleaseBashSession()`。
- 当前缺口是：如果 backend / runtime / sandbox 在初始化成功后再变成不可复用状态，registry 里的旧 entry 仍会继续存在并被复用；典型场景包括 tmux session 丢失、sandbox 容器被外部 kill、或后端内部状态损坏。
- 当前缺口会带来两个问题：一是后续命令可能持续命中已损坏的 executor；二是旧 backend / sandbox 没有被及时关闭，容易堆积无效容器和失效会话资源。

## Next Step

- 第一步：补上 session 级实例在“初始化成功后失效”的清理闭环；当 backend / runtime / sandbox 已损坏且不再适合继续复用时，立即把对应 entry 从 registry 移除并关闭旧 backend / sandbox。
- 第二步：让下次同一 session 的 `bash` 调用走重新创建路径，而不是继续复用已损坏实例；同时补针对 tmux 丢失、sandbox 外部销毁和后端状态损坏等场景的单元测试。
- 第三步：在上述最小失效清理闭环稳定后，再回头补更完整的 Docker 失败路径覆盖、工作区外路径暴露策略、只读挂载细节和审计字段完整性。
- 第四步：在最小闭环稳定后，再逐步补危险命令判定、执行拦截、更细粒度可读写范围限制、资源限制和审计增强。

## 分阶段推进

- 阶段 1：抽象与目录骨架。已完成 `Sandbox` / `Manager` / `Factory` 等最小接口和占位实现，生命周期与装配边界已初步落地。
- 阶段 2：最小可运行实现。已完成最小 Docker sandbox 落点、默认镜像 `golang:alpine`、容器内工作目录约定与基础命令执行能力。
- 阶段 3：与 terminal 装配。已完成统一 runtime 抽象、host/sandbox runtime 适配和 `TmuxBackend` 的 runner 注入；当前 terminal 只在“当前运行环境”执行命令，不显式感知 Docker。
- 阶段 4：安全控制增强。已完成最小目录边界、执行超时兜底、输出限制与 `pkg/observability` 审计闭环；更细粒度路径策略和更多失败路径覆盖仍在下一步。
- 阶段 5：资源与恢复能力。补 Memory / Process 限制、失败恢复、闲置清理和可观测性。

## Constraints

- `context.md` 只记录当前真实状态和明确下一步，不在这里提前写死完整理想架构。
- terminal 不应直接知道 sandbox 的具体实现细节；是否进入沙箱应由上层装配或策略层决定，而不是由 terminal 包内部动态判断。
- 同一个逻辑终端会话一旦绑定某种 runtime，就不应在同一个 `pane_id` 内中途在宿主机和沙箱之间来回切换。
- 第一阶段先追求最小闭环，不要同时引入多种 runtime、复杂权限系统和远程执行链路。
- 设计时要保留与现有 `TerminalBackend`、`TmuxBackend`、runner 注入点的兼容性，避免为了 sandbox 反向破坏已稳定的 terminal 语义。
- 所有实现都应优先可测试，避免把创建容器、路径映射、命令执行和策略判断耦合在一个结构体里。
- 当前这一步只补“已失效实例的摘除与关闭”，不在这里扩展成后台巡检、闲置回收、风险分级路由或跨 session 资源治理。
- 失效后的重建仍应沿用原有 route 和 working directory 约束；允许“同一 session 重新创建新实例”，但不允许静默切换成宿主机 fallback。
- 清理路径必须优先保证旧 entry 不再被 registry 继续返回，并确保旧 backend / sandbox 有明确关闭动作，避免重复复用已经损坏的实例。

## Code Anchor

- `internal/sandbox/sandbox.go`：sandbox 最小抽象定义。
- `internal/sandbox/manager.go`：sandbox 默认管理入口与占位工厂。
- `internal/sandbox/unimplemented.go`：当前阶段占位实现。
- `internal/sandbox/errors.go`：稳定错误定义。
- `internal/sandbox/docker.go`：最小 Docker runtime 和 CLI runner 适配。
- `internal/sandbox/docker_policy.go`：Docker sandbox 的目录边界、只读挂载、输出限制、截断与错误分类。
- `internal/sandbox/audit.go`：sandbox 执行审计。
- `internal/sandbox/runtime.go`：统一 runtime 抽象，以及 host runtime / sandbox runtime 适配。
- `internal/sandbox/terminal_runtime.go`：terminal 装配入口，显式绑定当前运行环境。
- `internal/sandbox/sandbox_test.go`：阶段 1、阶段 2、阶段 3、阶段 4 契约测试。
- `internal/tool/bash_session_registry.go`：`bash` 会话级 executor 注册、复用、失效摘除与关闭入口。
- `internal/core/session.go`：会话结束时的 `ReleaseBashSession()` 清理时机。
- `internal/tool/terminal/backend.go`：现有终端后端抽象，后续 sandbox 接入时必须兼容的能力面。
- `internal/tool/terminal/tmux_backend.go`：现有 tmux backend 壳层和 runner 适配入口。
- `internal/tool/terminal/tmux_pane_pool.go`：现有固定 `pane_id -> pane` 绑定、局部 reset 和 pane 生命周期主逻辑。
- `internal/tool/terminal/execute.go`：terminal 主执行编排、pending 生命周期和 backend 接线位置。
- `internal/tool/terminal/context.md`：terminal 当前上下文文档，可作为 sandbox 文档结构和边界约束的参考。
