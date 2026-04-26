# observability context

## 模块职责

`pkg/observability` 是 OpenTalon 的全局链路观测模块，负责统一管理 tracing 的配置、上下文传播、状态模型、导出和本地 trace 产物。

这个模块当前负责：

- 统一 `Tracer` / `Span` 抽象，避免业务层直接深度依赖原始 OTel SDK；
- 初始化和关闭全局 provider；
- 统一 Span 状态、属性、事件和错误记录语义；
- 支持 `stdout`、`jsonl`、`file`、`otlp` 四类 exporter；
- 组织本地 trace 目录，并生成 `spans.jsonl`、`summary.md`、`timeline.txt`；
- 提供 payload artifact 基础能力，使 payload 可以与 `trace_id` / `span_id` 绑定并落盘；
- 在属性导出和 payload 落盘前统一做脱敏。

## 不负责什么

这个模块不负责：

- 业务层 prompt 构造和模型响应解析；
- 业务层决定何时创建哪些 span；
- 把完整大 payload 直接作为普通 Span attributes 保存；
- 用 logger 承载完整请求/响应消息；
- 设计新的 tracing framework 或替代日志系统。

## 全局约束

- 必须保持现有 `Tracer` / `Span` 抽象稳定；
- 必须保持现有 exporter 主链路稳定；
- payload artifact 必须和 `trace_id` / `span_id` 强绑定；
- payload 落盘前必须统一经过 `Redactor`；
- 失败请求也应尽可能保留 payload；
- logger 只记录采集失败，不记录完整 payload。

## 关键代码锚点

- `config.go`
  - 配置加载、默认值、归一化。
- `provider.go`
  - 全局 provider 初始化、关闭、共享目录管理器。
- `tracer.go`
  - `Tracer` 抽象和 `StartSpan` 入口。
- `span.go`
  - `Span` 抽象和状态、属性、事件、错误行为。
- `context.go`
  - Span 与 context 的绑定和提取。
- `status.go`
  - 统一状态模型和错误到状态的映射。
- `redact.go`
  - 默认脱敏规则和递归脱敏。
- `exporter.go`
  - span 导出记录结构、`summary.md`、`timeline.txt`。
- `exporter_jsonl.go`
  - `spans.jsonl` 写入。
- `exporter_file.go`
  - 单个 span JSON 文件写入。
- `exporter_path.go`
  - process 目录、trace 目录和文件命名。
- `payload.go`
  - payload artifact 路径解析和 payload 写入。
- `payload_summary.go`
  - payload 大小统计、预览、哈希和截断摘要。
- `payload_test.go`
  - payload artifact 与 trace 目录共址、脱敏、原始 JSON 输入等行为测试。
- `payload_summary_test.go`
  - payload 摘要的成功、失败和边界条件测试。

## 当前状态

- 当前 tracing 主骨架已可用，agent、llm client、tool router、session 已接入；
- 当前本地 trace 目录已稳定生成 `spans.jsonl`、`summary.md`、`timeline.txt`；
- 当前 payload artifact 基础能力已可用，并且与 `timeline.txt` 位于同一 trace 目录；
- 当前 payload artifact 已支持结构化对象、原始 JSON 字符串、`[]byte` 和 `json.RawMessage`；
- 当前 payload 落盘前已统一经过 `Redactor`；
- 当前 payload 已支持稳定的大小统计、预览、哈希和截断标记；
- 当前针对 payload artifact 已有基础测试；
- 当前针对 payload 摘要也已有针对性测试；
- 当前 `doJSONRequest` 已接入非流式请求/响应 payload 采集，并会把摘要与 artifact 路径挂回 `llm.request` span；
- 当前流式请求也已接入“完成后统一落 request payload 与最终聚合 response payload”的采集逻辑；
- 当前 trace 仍然缺少 `summary.md` 对 payload artifact 的补充说明，以及旧 `internal/agent/trace.go` 的职责收敛。

## 已实现

- `Config` 默认值、环境变量读取和归一化；
- provider 初始化、重复初始化和关闭；
- `Tracer` / `Span` 抽象；
- `SpanStatus` 和 `StatusFromError`；
- 四类 exporter；
- 本地 trace 目录和时间线产物；
- 递归脱敏；
- payload artifact 基础写入与 trace 目录共址；
- payload artifact 基础测试；
- payload 摘要能力与相关测试。

## 待实现

- `summary.md` 或其他可读产物对 payload artifact 的补充说明；
- `prompt.build` 或其他更高层节点的 payload / prompt 摘要补充；
- 旧 `internal/agent/trace.go` 职责的迁移或下线。

## 当前关注点

当前下一步只聚焦一件事：

- 在 `summary.md` 或其他可读产物中补充 payload artifact 的可发现性说明。
