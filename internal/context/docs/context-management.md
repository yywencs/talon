# OpenTalon 上下文管理演进路线图

## 1. 目的与当前起点

OpenTalon 的一次模型请求由系统提示、示例和 `EventLog` 中的所有可映射事件组成。会话足够长时，用户消息、模型消息、工具调用和工具输出会持续累积，最终超过模型上下文窗口，或使成本、延迟和回答质量明显变差。

当前代码已经具备第一块基础能力：`internal/context.TokenTracker` 保存最近一次服务端 usage、会话累计 usage 和可配置的 `LLM_CONTEXT_WINDOW_TOKENS`。但是 `PromptBuilder` 仍会无条件读取完整 `EventLog`，窗口上限也尚未驱动裁剪或压缩。

本路线图的原则是：先获得可信的预算信号，再安全地处理历史，最后引入摘要。不要先接入模型摘要；在没有可靠计数和完整 turn 边界时，摘要只会掩盖上下文继续膨胀的问题。

## 2. 最终要达到的状态

完成后，每次请求模型前都能得到确定的上下文状态：当前活跃 token、窗口上限、自动压缩阈值、剩余预算和是否需要压缩。系统会优先保留最近且完整的任务过程；历史过长时先安全裁剪，必要时用摘要替代旧历史。任意一次裁剪都不会让工具调用与其观察结果分离。

最终请求路径如下：

```text
用户输入 / 工具观察
        │
        ▼
ContextManager 计算活跃 token 与预算状态
        │
        ├── 预算充足 ──► 选择完整历史
        │
        └── 达到阈值 ──► 裁剪旧完整 turn ──► 必要时生成/更新摘要
                                                   │
                                                   ▼
系统提示 + 示例 + 摘要 + 选中的事件 ──► PromptBuilder ──► LLM
```

这里的“活跃上下文”不是会话累计成本。它表示下一次请求会带给模型的历史规模，定义为：最近一次服务端报告的 `total_tokens`，加上那次模型输出之后本地新增的用户消息和工具观察的估算 token。会话累计 usage 继续只用于成本统计。

## 3. 分阶段实施

### 阶段 0：确认基础与边界（已完成）

保留现有 `TokenTracker`、`Session.SetContextWindowLimit` 和 `LLM_CONTEXT_WINDOW_TOKENS` 配置。此阶段不改变 prompt 内容，也不自动删除任何历史。

确认两条边界：

- `LLM_CONTEXT_WINDOW_TOKENS=0` 代表模型窗口未知；未知时绝不触发自动裁剪，但仍允许手动压缩。
- 服务端返回的 usage 可能为零或缺失；这不是“上下文为空”，而是“没有可信的服务端基线”。

验收：现有 `internal/context/token_tracker_test.go` 继续通过，且启动时能将配置的窗口值写入 Session。

### 阶段 1：建立可信的上下文预算

新增 `internal/context/budget.go`，定义只读的预算状态，例如：

```go
type ContextWindowStatus struct {
    ActiveTokens       int
    ContextWindowLimit int
    CompactAtTokens    int
    RemainingTokens    int
    LimitKnown         bool
    ShouldCompact      bool
}
```

同时为配置增加：

- `LLM_AUTO_COMPACT_TOKEN_LIMIT`：主动压缩阈值；为 `0` 时由窗口上限和输出预留推导。
- `LLM_CONTEXT_OUTPUT_RESERVE_TOKENS`：为模型本轮输出预留的 token；默认值在配置层统一给出，而不是散落在 Session 中。

阈值应取主动阈值与硬窗口安全阈值中更小的一个：

```text
安全阈值 = context_window_limit - output_reserve
compact_at = min(显式 auto_compact_limit, 安全阈值)
```

若其中一个值未知，则使用另一个；两者都未知时 `ShouldCompact` 必须为 `false`。

这一阶段还要补充增量估算。可以先采用稳定、可测试的 UTF-8 字节启发式（例如每 4 字节约 1 token），并明确它不是 tokenizer 精确计数。估算对象只包括服务端基线之后追加到 `EventLog` 的可见事件。

调整 `Session.Run` 的记账顺序：先写入本次模型生成的消息和 action 事件，再记录服务端 usage 与当前事件序号作为基线。之后的工具 observation 才作为本地增量计入下一次请求。这样不会把模型已经计入 `total_tokens` 的输出再次相加。

验收：

- 已知窗口下，`ActiveTokens`、`RemainingTokens` 与 `ShouldCompact` 对普通、临界和超限输入均有确定结果。
- 记录模型响应后，模型消息和 action 不会被重复估算；随后追加的 observation 会增加活跃 token。
- 窗口未知、负数配置、缺失 usage 和空事件日志均不会 panic，也不会误触发自动压缩。

### 阶段 2：让历史能够按完整 turn 安全处理

新增 `internal/context/history.go`（或 `history_manager.go`），负责把 `EventLog` 快照分组为 turn，并提供不可变的选择结果。`EventLog` 需要增加受锁保护的“快照版本号”和“原子替换/保留后缀”能力；`PromptBuilder` 不应在遍历完整日志时自行删除事件。

分组规则：

- 一条用户消息开启一个 turn。
- 同一 turn 中的 assistant 消息、`ActionEvent` 与对应 `ObservationEvent` 必须整体保留或整体移除。
- 启动前的系统类信息不属于可裁剪 turn；它们仍由 `PromptBuilder` 注入。
- 未配对的 action/observation 属于异常历史。第一版应保守地保留并记录诊断日志，不要静默丢弃信息。

此阶段只提供 `SelectRecentTurns`、`DropOldestCompleteTurn`、`ReplaceHistory` 等操作，不做摘要，也不改变默认策略。

验收：

- 删除最旧 turn 时，关联的 tool call 与 tool result 同时消失。
- 空历史、只有一条用户消息、只有工具事件和删除数量大于现有 turn 数都得到稳定结果。
- 并发读取 prompt 与写入事件时，通过 `go test -race`，不会读到半个 turn。

### 阶段 3：接入滑动窗口裁剪

在每次 `Agent.StreamStep` 之前调用预算计算。若 `ShouldCompact=false`，按原样构建 prompt；若为 `true`，反复删除最旧的完整 turn，直到估算值低于目标预算，或只剩不可再删的当前任务上下文。

阶段 3 不需要调用 LLM 做摘要。它是一个可预测的降级策略：即使模型服务不可用、摘要调用失败或用户关闭摘要功能，也能保证请求不会无限增长。

裁剪后的历史通过 ContextManager 返回给 `PromptBuilder`。不要让 `PromptBuilder` 直接修改 `EventLog`；是否持久替换原历史可由策略决定，但第一版推荐持久替换，并写入压缩事件或日志，以便会话恢复时状态一致。

验收：

- 连续模拟多轮工具调用后，传入 LLM 的 prompt 估算 token 不高于 `compact_at`。
- 最近一轮用户意图、其 action 与 observation 保留。
- 当只剩当前 turn 仍超限时，不循环删除；返回可观察的“单轮内容超过窗口”错误或警告。

### 阶段 4：引入摘要式压缩

新增一个小接口，而不是把模型调用写死在 Session：

```go
type Compactor interface {
    Compact(ctx context.Context, turns []Turn) (Summary, error)
}
```

默认实现可复用现有 LLM Client，但必须使用单独的、固定的摘要提示，并禁止摘要请求再次触发自动压缩。摘要应保留：用户目标、已完成与未完成工作、关键文件与命令结果、失败原因、用户明确约束。不要保存完整工具输出或模型思维链。

压缩步骤：

1. 选择足够旧的完整 turn 作为摘要输入。
2. 限制摘要输入长度，必要时先执行阶段 3 的安全裁剪。
3. 生成一个结构化摘要事件，替换这些旧 turn。
4. 保留摘要后的最近完整 turn，再重新计算预算。
5. 摘要失败时回退到阶段 3 的滑动窗口裁剪，并记录失败原因。

验收：

- 压缩成功后，旧 turn 被一个摘要事件替代，prompt token 明显下降。
- 摘要服务报错、超时或返回空内容时，主会话不会失败，仍会使用滑动窗口继续。
- 连续两次压缩不会把已有摘要无限嵌套；应更新已有摘要或设置最大摘要层数为 1。

### 阶段 5：完善生命周期、可观测性和人工控制

增加手动压缩入口、压缩前后回调和结构化观测字段：`context.active_tokens`、`context.compact_at_tokens`、`context.trimmed_turns`、`context.summary_tokens`、`context.compaction_reason` 与耗时。CLI 至少要能显示一次压缩已经发生，而不暴露摘要中的敏感工具输出。

可选增加会话级 context window ID；每次压缩开启新窗口，日志可关联“本窗口从何时开始、预填了多少 token”。这项能力便于长期排障，但不是阶段 1 到 4 的前置条件。

验收：

- 自动与手动压缩均可区分原因，且每次只记录一次完成事件。
- 取消会话或压缩失败不会留下半替换的 `EventLog`。
- 压缩相关测试覆盖正常、边界、错误和并发场景；全量 `go test ./...` 与 `go test -race ./...` 均通过。

## 4. 文件改动顺序

| 顺序 | 文件 | 改动目的 |
| --- | --- | --- |
| 1 | `internal/context/budget.go` | 计算预算状态、阈值与剩余 token |
| 2 | `internal/context/token_tracker.go` | 保存服务端基线对应的事件版本，并暴露增量估算所需数据 |
| 3 | `internal/types/event_log.go` | 增加版本化快照和原子历史替换能力 |
| 4 | `internal/context/history.go` | 将事件分组为完整 turn，提供选择与裁剪操作 |
| 5 | `internal/core/session.go` | 调整 usage 记录时机，并在每次模型调用前执行预算检查 |
| 6 | `internal/agent/prompt_builder.go` | 只接受 ContextManager 选中的历史，而不是自行读取完整日志 |
| 7 | `pkg/config/config.go`、`.env.example`、`README.md` | 暴露阈值配置和运行说明 |
| 8 | `internal/context/compactor.go` | 在滑动窗口稳定后接入摘要与失败回退 |

每完成一行先补对应单测，再把下一行接入运行路径。阶段 1 和阶段 2 未通过测试前，不开始阶段 3；阶段 3 在真实长会话中验证前，不开始阶段 4。

## 5. 本轮不做的内容

参考 `reference/context-governance` 中的远程压缩、世界状态 diff、图像输入降级、分支/回滚 transcript 重放、协作模式指令 diff 和多种 provider 专用重试都很有价值，但依赖 OpenTalon 当前尚未提供的协议模型。第一版不复制这些实现。

第一版的完成标准不是“功能数量接近参考项目”，而是满足以下不变量：

- 任何发给 LLM 的历史都受可解释的 token 预算约束。
- 不会把一次工具调用与其结果拆开。
- 自动压缩失败时会安全降级，而不是让整个 Agent loop 失败。
- token 成本统计与活跃上下文估算语义分离，且每个数值都有来源。
