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

## Next Step

- 第一步：把显式的“会话结束/主动关闭”释放入口补到更完整的 session 生命周期中，而不只依赖 `Run()` 正常结束或报错退出。
- 第二步：把默认 sandbox 镜像切到预装 `tmux`、`bash`、`git` 等依赖的自定义镜像，并补充同 session 复用、跨 session 隔离、失败清理和 session 结束回收的测试。

## Constraints

- `context.md` 只记录当前真实状态和明确下一步，不在这里提前展开理想化架构。
- terminal 层只负责复用“当前运行环境”的 executor/backend/runtime，不应直接下沉 Docker 细节、镜像安装逻辑或复杂权限策略。
- 同一个逻辑会话一旦绑定某个 terminal executor / runtime / sandbox，就不应在同一会话生命周期内中途切换到其他 runtime 或其他容器。
- `session.id` 应作为 terminal 会话级复用的主键；不得仅通过固定容器名字符串来伪装复用。
- sandbox 初始化失败时不得静默回退到宿主机 runtime；失败语义必须稳定且可清理。
- 保持当前 `pane_id` 语义不变；如果上层已显式传入 `pane_id`，工具层不得再覆盖为固定默认值。
- 当前阶段先解决会话级复用、依赖对齐和失败清理，不同时展开 remote runtime、agent-server、多种 sandbox 池化或命令策略引擎。
- 所有改造都应优先可测试，避免把 session 注册表、executor 生命周期、backend 状态与 Docker 清理逻辑耦合在一个巨大结构体中。

## Code Anchor

- `internal/tool/terminal_tool.go`：当前 `bash` 工具兼容入口；默认 sandbox 路径和重复创建 `DockerSandbox` 的直接入口。
- `internal/tool/terminal/execute.go`：terminal 主执行编排、pending 生命周期、`PrepareCommand` / `CompleteCommand` 调用位置。
- `internal/tool/terminal/backend.go`：统一 `TerminalBackend` 能力面和可选生命周期接口。
- `internal/tool/terminal/tmux_backend.go`：`TmuxBackend.Initialize()`、runner `Prepare()`、`tmux`/`bash` 依赖检查与 session 生命周期。
- `internal/sandbox/runtime.go`：host runtime / sandbox runtime 统一抽象，以及 sandbox runtime 的 `Prepare()` / `Close()` 行为。
- `internal/sandbox/terminal_runtime.go`：terminal 与 sandbox runtime 的装配入口。
- `internal/sandbox/docker.go`：Docker sandbox 的启动、关闭、执行和容器参数组装。
- `internal/sandbox/sandbox.go`：默认镜像、工作目录和 sandbox 基础配置。
- `internal/core/session.go`：会话主循环与 `session.id` 来源；后续 terminal 会话级复用需要围绕这里的会话生命周期接线。
- `internal/types/state.go`：`SessionState.ID` 定义，是 terminal/sandbox 会话级复用的稳定主键来源。
