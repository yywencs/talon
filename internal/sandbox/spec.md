# sandbox default docker routing SPEC

## Scope

- 本文件只约束当前这一步的实现：由上层装配层决定“当前运行环境”，并把默认工具入口切到 Docker sandbox。
- 当前阶段目标是：在不破坏已有 runtime 抽象和 terminal 无感装配语义的前提下，让工具默认运行在 Docker sandbox，而不是宿主机 tmux backend。
- 当前阶段只解决以下问题：
  - 收敛默认运行环境选择入口，明确默认路径走 sandbox runtime
  - 默认创建并使用 Docker-backed sandbox 作为工具执行环境
  - 保留宿主机 runtime 作为显式、可控的非默认路径
  - 明确 Docker sandbox 准备失败时的稳定返回语义，避免静默回退到宿主机
- 当前阶段继续复用现有 `internal/sandbox/runtime.go`、`internal/sandbox/terminal_runtime.go`、`internal/sandbox/manager.go` 和 terminal backend 注入能力，不在 `internal/tool/terminal` 包内新增 Docker 感知逻辑。
- 当前阶段不要求实现风险分级自动选择、危险命令判定、资源限制增强、恢复编排、闲置清理或新的安全策略下沉。

## Constraints

- 当前阶段不得破坏阶段 4 已完成的目录边界、统一超时、输出截断与审计语义；默认切换到 Docker sandbox 后，这些能力必须继续生效。
- 当前阶段不得破坏阶段 3 已完成的 runtime 抽象与 terminal 无感知装配语义；`internal/tool/terminal` 不得显式判断“当前是不是 Docker”。
- 默认路径必须明确指向 Docker sandbox；除显式配置或显式调用宿主机入口外，不得继续让宿主机 runtime 作为默认行为。
- Docker sandbox 创建、装配或预热失败时，必须返回稳定错误；不得为了“可用性”静默降级到宿主机执行，以免破坏隔离预期。
- 宿主机 runtime 可以保留，但只能作为显式、可测试的非默认路径；不得通过隐藏分支在同一套默认入口里自动回退。
- 同一个逻辑终端会话一旦绑定某种 runtime，就不得在同一个 `pane_id` 内中途在宿主机和 sandbox 之间切换。
- 当前阶段不下沉风险判定；是否需要未来引入风险分级、按命令选择 host/sandbox，仍由上层装配策略后续决定。
- 当前阶段优先复用已有构造和注入点；若新增导出类型、接口、配置或构造函数，必须补中文 GoDoc 注释，并保持 Go 风格命名，若无必要不要新增文件。

## Definition of Done

- 默认工具入口会创建并使用 Docker sandbox 对应的 runtime / backend，默认命令执行不再直接落到宿主机 tmux backend。
- 现有宿主机 runtime 入口仍可保留，但必须是显式路径，语义上清楚区分“默认 sandbox”与“显式 host”。
- terminal 包仍只消费统一 backend / runner 能力；代码结构上不引入 Docker 特化判断，也不把风险判定下沉到 terminal 内部。
- 默认 Docker 路径在 sandbox 创建或预热失败时会稳定返回错误，不会静默回退到宿主机继续执行。
- 相关单元测试能够断言默认路径、显式 host 路径和失败语义，避免回归到“默认宿主机执行”。
- `internal/sandbox/context.md` 与 `internal/sandbox/spec.md` 保持一致，不出现范围漂移。

## Test Contract

- 当前阶段测试以单元测试为主；涉及真实 Docker 的测试必须默认跳过，并通过显式环境变量开启。
- 至少覆盖以下场景：
  - 默认工具入口会选择 sandbox runtime / Docker backend，而不是宿主机 backend
  - 显式宿主机入口仍可工作，且不会被默认路径误复用
  - 默认 Docker 路径会触发 sandbox 创建或预热逻辑
  - sandbox 创建或预热失败时返回稳定错误，且不会静默回退到宿主机执行
  - terminal 包仍通过统一 runner / backend 执行，不需要显式知道 Docker 细节
- 禁止为了覆盖率编写低价值样板测试；优先验证默认路由、失败语义和装配边界是否稳定。
