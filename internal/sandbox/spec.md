# sandbox session invalidation cleanup SPEC

## Scope

- 本文件只约束当前这一步的实现：为默认 `bash` 的 session 级 sandbox 实例补上“失效即摘除并关闭”的清理闭环。
- 当前阶段目标是：在不破坏已有 runtime 抽象、terminal 无感装配语义和默认 Docker 路径的前提下，让已损坏的 backend / runtime / sandbox 不再被继续复用。
- 当前阶段只解决以下问题：
  - 明确 session 级 executor / backend / sandbox 在“初始化成功后失效”时的清理语义
  - 在确认实例已经不可继续复用时，把对应 session entry 从 registry 移除
  - 在 entry 摘除后立即 close 旧 backend / sandbox，避免堆积失效容器
  - 让同一 session 的下次调用重新创建新实例，而不是继续命中已损坏对象
- 当前阶段继续复用现有 `internal/tool/bash_session_registry.go`、`internal/sandbox/runtime.go`、`internal/sandbox/terminal_runtime.go`、`internal/sandbox/manager.go` 和 terminal backend 注入能力，不在 `internal/tool/terminal` 包内新增 Docker 感知逻辑。
- 当前阶段不要求实现后台巡检、闲置回收、跨 session 资源清扫、风险分级路由、危险命令判定或新的安全策略下沉。

## Constraints

- 当前阶段不得破坏阶段 4 已完成的目录边界、统一超时、输出截断与审计语义；补失效清理后，这些能力必须继续生效。
- 当前阶段不得破坏阶段 3 已完成的 runtime 抽象与 terminal 无感知装配语义；`internal/tool/terminal` 不得显式判断“当前是不是 Docker”。
- 默认路径仍必须明确指向 Docker sandbox；除显式配置或显式调用宿主机入口外，不得让“失效后重建”变成宿主机 fallback。
- Docker sandbox 创建、装配、预热或后续生命周期失效时，必须返回稳定错误；不得为了“可用性”静默降级到宿主机执行，以免破坏隔离预期。
- 宿主机 runtime 可以保留，但只能作为显式、可测试的非默认路径；不得通过隐藏分支在失效清理路径里自动回退。
- 同一个逻辑终端会话一旦绑定某种 runtime，就不得在同一个 `pane_id` 内中途在宿主机和 sandbox 之间切换；若旧实例已失效，只允许在同一路由上重新创建新的 sandbox 实例。
- 失效摘除必须以“该 session 级实例已不适合继续复用”为前提，不能把普通命令失败、业务命令退出码非 0 或一次性可恢复错误都升级成强制销毁。
- 清理路径必须先把 entry 从 registry 中摘除，再关闭旧 backend / sandbox，避免并发请求继续拿到旧实例或对同一个实例重复复用。
- 关闭旧 backend / sandbox 时必须尽量幂等，避免因为重复失效回调或并发清理导致二次 panic、重复 close 或新实例被误删。
- 当前阶段优先复用已有构造和注入点；若新增导出类型、接口、配置或构造函数，必须补中文 GoDoc 注释，并保持 Go 风格命名，若无必要不要新增文件。

## Definition of Done

- session 级 `bash` entry 在确认 backend / runtime / sandbox 已失效后，会从 registry 中移除，后续调用不再继续复用旧实例。
- 旧 entry 被移除后会立即触发 backend / sandbox 的关闭动作，避免失效容器和无效 tmux 会话持续堆积。
- 同一 session 在旧实例失效后再次调用 `bash` 时，会重新创建新的 sandbox 实例，并继续保持默认 Docker 路径语义。
- terminal 包仍只消费统一 backend / runner 能力；代码结构上不引入 Docker 特化判断，也不把失效判定下沉成 terminal 对 Docker 的显式分支。
- Docker 路径在初始化失败和初始化后生命周期失效时都会稳定返回错误，不会静默回退到宿主机继续执行。
- 相关单元测试能够断言失效摘除、立即关闭、下次重建和无宿主机回退等语义，避免回归到“默认宿主机执行”或“继续复用坏实例”。
- `internal/sandbox/context.md` 与 `internal/sandbox/spec.md` 保持一致，不出现范围漂移。

## Test Contract

- 当前阶段测试以单元测试为主；涉及真实 Docker 的测试必须默认跳过，并通过显式环境变量开启。
- 至少覆盖以下场景：
  - session 级 entry 在初始化失败时仍会按现有语义失效摘除并关闭
  - session 级 entry 在初始化成功后，如果 tmux session 丢失、sandbox 容器被外部 kill 或后端状态损坏等不可复用场景出现，会触发失效摘除并关闭
  - 失效摘除后，同一 session 的下一次 `bash` 调用会重新创建新的 sandbox backend / runtime，而不是继续返回旧 entry
  - 清理路径不会静默回退到宿主机执行，也不会误删已经新建的 replacement entry
  - terminal 包仍通过统一 runner / backend 执行，不需要显式知道 Docker 细节
- 禁止为了覆盖率编写低价值样板测试；优先验证失效摘除、立即关闭、并发安全和无宿主机回退等核心语义是否稳定。
