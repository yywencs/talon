# sandbox phase-3 terminal runtime integration SPEC

## Scope

- 本文件只约束 `internal/sandbox` 的阶段 3 实现：与 terminal 的运行环境装配。
- 当前阶段的目标是：让 terminal 在“当前运行环境”里执行命令，但 terminal 本身不需要知道底层是不是 Docker。
- 当前阶段只解决以下问题：
  - 如何把宿主机 runtime 或 sandbox runtime 统一抽象成 terminal 可消费的最小执行能力
  - 如何在 terminal / tmux backend 创建时注入当前运行环境
  - 如何保持 terminal 现有 `pane_id`、`is_input`、`reset`、固定 pane 语义不变
  - 如何确保同一条 terminal 会话在整个生命周期内绑定同一种 runtime
- 当前阶段允许复用阶段 2 已有的 Docker sandbox，但 Docker 只是某种 runtime 实现，不应暴露为 terminal action 级概念。
- 当前阶段不要求接入风险分级自动切换，不要求实现完整权限或资源限制，也不要求让所有工具立即迁移到 sandbox。

## Constraints

- `terminal` 必须继续只面向“当前运行环境”执行命令；不得在 action、observation 或工具描述中新增 `docker=true`、`runtime=sandbox` 之类的显式字段。
- 当前阶段不得修改 terminal 的既有行为语义，包括 `pane_id`、`is_input`、`reset`、固定 pane 绑定、pending 生命周期和 observation 结构。
- 同一条 terminal 会话一旦创建并绑定某种 runtime，在该会话生命周期内不得在宿主机 runtime 与 sandbox runtime 之间中途切换。
- 当前阶段不得要求大模型在每条 terminal 命令上显式声明运行位置；运行位置应由上层装配层在会话创建或 backend 初始化时决定。
- 当前阶段允许 `internal/tool/terminal` 依赖一个最小执行能力接口，但不得直接依赖 `DockerSandbox` 具体类型或 Docker 细节。
- 当前阶段允许引入 host runtime 与 sandbox runtime 两种实现，但它们必须通过统一最小接口暴露能力，例如运行命令、查找二进制或等价 runner 能力。
- 当前阶段不得在 `terminal_tool.go`、`execute.go`、`TmuxBackend` 中加入“按风险等级自动切换到 sandbox”的业务逻辑；风险判定和 runtime 选择必须留在更上层装配代码。
- 当前阶段不得实现危险命令识别、权限策略、目录读写白名单、CPU / Memory / Process 限制、网络隔离、审计增强或复杂恢复机制。
- 当前阶段与 terminal 的装配必须保持最小，只覆盖后续 `TmuxBackend` 真正需要的执行能力；不要提前扩张成完整 runtime 管理框架。
- 当前阶段若 `TmuxBackend` 需要 runner 注入，必须保持其对宿主机和 sandbox 的无感知；它只知道当前 runner 能执行命令，不知道底层是不是 Docker。
- 当前阶段默认 Docker 镜像仍使用 `golang:alpine`；但这属于 sandbox runtime 内部默认值，不得泄漏为 terminal 的显式配置语义。
- 当前阶段容器启动、命令执行和 runner 注入逻辑必须可测试；测试应优先使用 mock runner，避免依赖真实 Docker 环境。
- 当前阶段允许继续保留默认占位工厂或默认宿主机行为，但必须能显式构造“绑定 sandbox runtime 的 terminal backend”。
- 当前阶段若新增导出类型、接口或构造函数，必须补中文 GoDoc 注释，并保持 Go 风格命名。
- 当前阶段若新增 `context.md` 关联文件或目录结构，必须同步反映真实状态；禁止在文档中描述尚未实现的能力为“已完成”。

## Definition of Done

- `internal/sandbox` 与 `internal/tool/terminal` 之间存在一组明确的最小执行能力边界，可支撑 terminal 在“当前运行环境”里执行命令。
- 至少存在一组统一抽象，用于表达：
  - 宿主机运行环境
  - sandbox 运行环境
  - terminal 可消费的最小 runner / runtime provider 能力
- `TmuxBackend` 或其等价适配层可以在不感知 Docker 的前提下，使用注入的当前 runtime 执行 `tmux` 命令。
- terminal 的 action、observation 和交互语义保持兼容，不需要新增显式 Docker 字段。
- 至少存在一种显式装配方式，可以创建“宿主机 terminal backend”和“sandbox terminal backend”，但二者对 terminal 调用面保持一致。
- 同一条 terminal 会话在创建后会稳定绑定其 runtime，不会在执行途中漂移。
- 若当前 runtime 初始化、命令执行或关闭失败，错误行为必须稳定、可预期、可测试。
- `internal/sandbox/context.md` 与 `internal/sandbox/spec.md` 保持一致，不出现范围漂移。

## Test Contract

- 当前阶段测试以单元测试为主，禁止依赖真实 Docker、真实 tmux 或外部系统环境。
- 测试重点应放在 runtime 抽象、runner 注入和 terminal 侧无感知装配，而不是完整容器集成。
- 至少覆盖以下场景：
  - host runtime 与 sandbox runtime 均可被适配成同一种最小执行接口
  - terminal backend 创建时可注入不同 runtime，而无需修改 action
  - 注入 sandbox runtime 后，terminal 侧调用面不出现 Docker 显式语义
  - 同一条会话在一次创建后会持续使用同一个 runtime
  - runtime 命令执行失败时返回稳定且可断言的错误
- 如果补充真实 Docker 集成测试，必须默认跳过，并通过显式环境变量开启。
- 当前阶段禁止为了覆盖率而写低价值样板测试；优先测试 runner 注入、runtime 绑定和 terminal 无感知语义。
