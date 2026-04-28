# Context

## Current State

- `internal/agent` 负责把会话事件转换成 LLM 消息、调用不同 provider、解析模型输出，并把 tool call 转成上层可执行的语义结果。
- 当前 function calling 主路径已经接入 `OpenAI-compatible` 协议：请求会携带 `tools`，响应会解析 `tool_calls`，流式场景也会拼接增量 tool call。
- 当前 `Ollama` 实现仍是纯文本 chat/stream chat，只发送普通消息，不发送工具 schema，也不解析工具调用；遇到不支持 function calling 的模型时没有兜底。
- 当前 `Agent.ParseLLMResponse` 只信任响应体里的原生 `ToolCalls` 字段，没有从纯文本中恢复工具意图，因此模型不支持 function calling 或产生幻觉格式时容易直接退化成普通文本。
- 当前还没有“模型能力注册表”；系统无法按具体模型判断是否支持 function calling、reasoning、vision 等能力，相关能力判断未来不能继续散落在 provider 细节里。

## File Map

- `agent.go`: Agent 主流程；构建消息、发起流式请求、把 LLM 响应转成 `AgentTurnResult`，并处理 `finish` 截断。
- `agent_test.go`: 校验 `finish` 截断、工具调用场景下 reasoning 搬运等 Agent 语义。
- `llm_client.go`: 统一 `LLMClient` 抽象、请求/响应结构、provider 工厂和基础 span 信息。
- `llm_client_http.go`: 通用 HTTP JSON 请求、重试退避、payload 观测、endpoint 补全。
- `llm_client_message.go`: provider 间的消息转换辅助；OpenAI 序列化、流式 tool call 拼接、cache 标记清理。
- `llm_client_ollama.go`: Ollama chat/stream chat 实现；当前只处理文本内容和 token 统计。
- `llm_client_openai.go`: OpenAI-compatible chat/stream chat 实现；已支持 `tools` 请求和 `tool_calls`/`reasoning_content` 解析。
- `llm_client_test.go`: LLM client 的 live test 与本地 mock 测试，覆盖流式聚合、payload 观测、序列化等行为。
- `prompt_builder.go`: 把 `SessionState.Events` 转成模型消息序列，并给稳定前缀/滚动上下文打缓存标记。
- `prompt_builder_test.go`: 校验 prompt 构建后的消息顺序与事件映射。
- `trace.go`: 旧式请求/响应追踪能力，负责落盘 trace 文件。
- `spec_tool_router.md`: 早期 tool router 设计草案，描述工具解析、并发执行和异常边界，不是当前代码真相。

## Next Step

- 为 function calling 增加兜底层，核心目标是兼容两类失败：模型本身不支持原生 function calling；模型返回了错误/幻觉化的 tool call 格式。
- 兜底入口优先收敛在 `Agent.ParseLLMResponse` 或紧邻的响应适配层，同时为后续“模型能力注册表”预留统一判断点。
- 兜底策略应至少支持：原生 `ToolCalls` 正常时继续走主路径；原生缺失时尝试从文本中恢复结构化工具调用；恢复失败时保留文本消息而不是报无效结果。
- 下一阶段应补一个模型能力注册表，用来按模型判断是否支持 function calling 等能力，并决定是否启用原生能力、fallback 或纯文本降级路径。
- 补充针对 fallback 的单元测试，优先覆盖 OpenAI 无 `tool_calls`、Ollama 纯文本伪工具调用、混合文本加工具调用三类场景。

## Constraints

- 不要破坏现有 OpenAI-compatible 原生 function calling 主路径。
- 不要在 provider 底层硬编码业务超时；继续由入口 `ctx` 透传控制。
- `context.md` 只记录真实现状，不把 `spec_tool_router.md` 中未落地设计当成已实现能力。
- fallback 必须是保守增强：无法确认是工具调用时，宁可保留普通文本，也不要误触发危险工具。
- 后续能力判断应集中到模型能力注册表，避免继续在 `agent` 与各个 `llm_client_*` 文件中散落模型特判。

## Code Anchor

- `internal/agent/agent.go`
- `internal/agent/llm_client_openai.go`
- `internal/agent/llm_client_ollama.go`
- `internal/agent/llm_client_message.go`
- `internal/agent/llm_client_test.go`
- `internal/agent/spec.md`
