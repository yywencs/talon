# sandbox context
## 各文件作用

- `context.md`：记录 sandbox 当前真实状态、下一步实现顺序、约束和代码锚点。
- `spec.md`：约束 sandbox 当前阶段实现范围，防止 runtime 装配和 terminal 接线过度扩张。
- `sandbox.go`：定义 sandbox 最小核心抽象，包括状态、配置、实例接口和工厂接口。
- `manager.go`：提供面向上层装配层的最小创建入口，并封装默认占位工厂。
- `unimplemented.go`：提供当前阶段的 sandbox 占位实现，统一未实现生命周期行为。
- `errors.go`：定义 sandbox 阶段 1 需要暴露的稳定错误。
- `docker.go`：提供基于 Docker CLI runner 的最小真实 sandbox runtime，实现启动、关闭和容器内命令执行。
- `runtime.go`：提供“当前运行环境”的统一抽象，收敛 host runtime 与 sandbox runtime 的最小执行能力。
- `terminal_runtime.go`：提供面向 terminal 的最小装配入口，显式创建绑定 host runtime 或 sandbox runtime 的 tmux backend。
- `sandbox_test.go`：覆盖阶段 1、阶段 2 与阶段 3 的契约测试，包括默认镜像、Docker 参数组装、runtime 绑定和 terminal 无感知语义。

## 当前状态

- `internal/sandbox` 已完成阶段 1、阶段 2，并已落地阶段 3 的最小 runtime 装配：当前已经具备抽象接口、默认管理入口、占位实现、最小 Docker runtime、统一 runtime 抽象和 terminal 装配入口。
- 当前目标是让工具执行从“直接运行在宿主机”演进为“具备隔离、权限控制、审计与恢复能力的可控执行系统”。
- 当前 terminal 能力已经基本收拢到 `internal/tool/terminal`：命令执行、交互式输入、`reset`、超时、输出截断、固定 `pane_id` 绑定和 backend 抽象都已具备。
- 当前 terminal 默认仍使用宿主机 tmux backend，但 `TmuxBackend` 已新增可注入的当前运行环境 runner，后端本身不需要知道底层是不是 Docker。
- 当前 `internal/sandbox/runtime.go` 已把宿主机与 sandbox 收敛成统一的最小执行能力；host runtime 与 sandbox runtime 都可以作为“当前运行环境”注入 terminal。
- 当前 `terminal_tool.go` 仍是兼容入口，`pane_id` 暂时可先固定，当前不需要先解决多 pane 调度问题才能开始 sandbox 基础建设。
- 当前 sandbox 已支持最小 Docker runtime：可以创建 Docker sandbox、按默认镜像 `golang:alpine` 启动容器、约定容器内工作目录并执行基础命令。
- 当前真实 runtime 仍保持收敛：基于可 mock 的 Docker CLI runner 实现，且 terminal 侧只消费统一 runner 能力，不暴露 Docker 显式语义。
- 当前已提供显式装配入口，可以创建绑定 host runtime 或 sandbox runtime 的 tmux backend；但默认工具入口尚未切换到 sandbox，也尚未引入风险分级自动选择。

## Next Step

- 第一步：补更完整的阶段 3 测试，继续覆盖更多 runtime 失败路径、host/sandbox 绑定行为和 terminal 组合时的稳定性。
- 第二步：决定上层装配层如何选择当前运行环境，并把默认工具入口切换策略收敛出来，但暂时不把风险判定下沉到 terminal 包内部。
- 第三步：在最小闭环稳定后，再逐步补危险命令判定、执行拦截、可读写范围限制、资源限制和审计增强。

## 分阶段推进

- 阶段 1：抽象与目录骨架。已完成 `Sandbox` / `Manager` / `Factory` 等最小接口和占位实现，生命周期与装配边界已初步落地。
- 阶段 2：最小可运行实现。已完成最小 Docker sandbox 落点、默认镜像 `golang:alpine`、容器内工作目录约定与基础命令执行能力。
- 阶段 3：与 terminal 装配。已完成统一 runtime 抽象、host/sandbox runtime 适配和 `TmuxBackend` 的 runner 注入；当前 terminal 只在“当前运行环境”执行命令，不显式感知 Docker。
- 阶段 4：安全控制增强。补目录权限、超时兜底、输出限制与审计字段。
- 阶段 5：资源与恢复能力。补 CPU / Memory / Process 限制、失败恢复、闲置清理和可观测性、命令拦截。

## Constraints

- `context.md` 只记录当前真实状态和明确下一步，不在这里提前写死完整理想架构。
- terminal 不应直接知道 sandbox 的具体实现细节；是否进入沙箱应由上层装配或策略层决定，而不是由 terminal 包内部动态判断。
- 同一个逻辑终端会话一旦绑定某种 runtime，就不应在同一个 `pane_id` 内中途在宿主机和沙箱之间来回切换。
- 第一阶段先追求最小闭环，不要同时引入多种 runtime、复杂权限系统和远程执行链路。
- 设计时要保留与现有 `TerminalBackend`、`TmuxBackend`、runner 注入点的兼容性，避免为了 sandbox 反向破坏已稳定的 terminal 语义。
- 所有实现都应优先可测试，避免把创建容器、路径映射、命令执行和策略判断耦合在一个结构体里。

## Code Anchor

- `internal/sandbox/sandbox.go`：sandbox 最小抽象定义。
- `internal/sandbox/manager.go`：sandbox 默认管理入口与占位工厂。
- `internal/sandbox/unimplemented.go`：当前阶段占位实现。
- `internal/sandbox/errors.go`：稳定错误定义。
- `internal/sandbox/docker.go`：最小 Docker runtime 和 CLI runner 适配。
- `internal/sandbox/runtime.go`：统一 runtime 抽象，以及 host runtime / sandbox runtime 适配。
- `internal/sandbox/terminal_runtime.go`：terminal 装配入口，显式绑定当前运行环境。
- `internal/sandbox/sandbox_test.go`：阶段 1、阶段 2、阶段 3 契约测试。
- `internal/tool/terminal/backend.go`：现有终端后端抽象，后续 sandbox 接入时必须兼容的能力面。
- `internal/tool/terminal/tmux_backend.go`：现有 tmux backend 壳层和 runner 适配入口。
- `internal/tool/terminal/tmux_pane_pool.go`：现有固定 `pane_id -> pane` 绑定、局部 reset 和 pane 生命周期主逻辑。
- `internal/tool/terminal/execute.go`：terminal 主执行编排、pending 生命周期和 backend 接线位置。
- `internal/tool/terminal/context.md`：terminal 当前上下文文档，可作为 sandbox 文档结构和边界约束的参考。
