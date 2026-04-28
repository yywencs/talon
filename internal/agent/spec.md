# Function Calling Fallback Spec

## Scope

- 本次改动只解决 `internal/agent` 内部的 function calling 兜底，不扩展到新的 tool router、执行框架或会话调度重构。
- 目标场景只包含两类失败：模型不支持原生 function calling，响应里没有 `ToolCalls`；模型产生了“看起来像工具调用”的文本，但原生结构缺失、格式不稳定或存在幻觉。
- 兜底只作用于“LLM 响应解析为 `AgentTurnResult`”这一层，优先落在 `Agent.ParseLLMResponse` 或紧邻的 provider 适配层。
- 现有 OpenAI-compatible 原生 `tools` / `tool_calls` 路径继续作为第一优先级主路径，不得被 fallback 覆盖。
- 本次实现要为后续“模型能力注册表”预留演进空间，使系统未来可以按模型判断 function calling、reasoning、vision 等能力是否可用。

## Required

- 必须先判断原生 `ToolCalls` 是否存在；只要原生结果有效，就直接返回，不再进入文本兜底解析。
- 必须将 fallback 设计成保守恢复，只有在能明确识别为结构化工具调用时，才生成 `[]types.MessageToolCall`；无法确认时，保留为普通文本消息。
- 必须支持混合内容；如果文本中既有说明文字又有可确认的工具调用，保留文本并返回工具调用，不得因为识别出工具调用就无条件丢弃正常说明文本。
- 必须保证 `finish` 截断逻辑仍然生效；fallback 产出的工具调用进入现有 `toolCallBatch.truncateAtFinish()` 流程。
- 必须保证返回结果稳定；`responseToTurnResult()` 在 fallback 命中时，仍返回合法的 `Message` / `ToolCalls` / `Finished` 组合，不得因为 fallback 解析失败，把原本可显示的文本升级成错误。
- 必须把实现边界收敛在 `internal/agent` 目录内；不要把 fallback 逻辑分散注入工具执行层、会话状态层或外部包。
- 必须为未来能力判断预留统一入口，后续模型能力注册表接入时，不应需要再次拆散 `agent` 与 `llm_client_*` 的职责边界。

## Not In Scope

- 不做新的 Tool Router 设计，不引入新的并发执行抽象。
- 不修改工具 schema 生成方式，不重写 `toolpkg.ToolsToOpenAITools(...)`。
- 不为 Ollama 新增私有协议扩展，不假设 Ollama 原生支持 OpenAI 风格 `tools`。
- 不在 provider 层增加新的业务超时、重试策略或长链路控制逻辑。
- 不做 prompt 大改，不在这次 spec 里要求重新设计 system prompt 或 few-shot 模板。
- 不尝试自动修复明显危险或不完整的工具参数；宁可降级为普通文本。
- 不把“模型是否支持某能力”的判断继续散落成 provider 名称判断、模型名字符串特判或多处布尔开关。

## Capability Registry Direction

- 后续应新增“模型能力注册表”，按模型维度集中描述能力，而不是只按 provider 粗略判断。
- 注册表的职责应至少包括：判断是否支持原生 function calling，必要时再扩展到 reasoning、vision、json mode 等能力。
- fallback 解析与能力注册表的关系应为：先根据能力注册表判断当前模型是否应尝试原生 function calling；如果模型声明不支持或未声明支持，则允许直接走 fallback / 纯文本降级路径；如果模型声明支持原生 function calling，但响应缺失原生 `ToolCalls`，仍可触发 fallback 作为兜底。
- 能力注册表应是集中决策点，不应改变 `llm_client` 负责协议适配、`agent` 负责语义决策的总体分层。

## Fallback Rules

- 原生优先：`resp.Message.ToolCalls` 非空时，直接使用原生结果。
- 能力判断优先于请求策略选择：后续接入模型能力注册表后，应由统一能力判断决定某个模型是否发送原生 `tools`。
- 文本兜底触发条件：原生 `ToolCalls` 为空，且 `resp.Message.Content` 中存在非空纯文本。
- 文本兜底识别目标：明确的工具名、明确可解析的参数 JSON，以及与现有 `types.MessageToolCall` 对齐的最小字段 `Name` / `Arguments`；`ID` 可为空或按现有策略补齐。
- 文本兜底降级条件：工具名缺失、参数不是合法 JSON、文本内容歧义过高无法确认是否真的是工具调用，或内容更像自然语言建议而不是待执行动作。
- 安全原则：fallback 只做识别，不做猜测补全；不根据模糊语义自行推导命令、路径、参数默认值。

## Implementation Constraints

- 优先新增一个局部、可测试的 fallback 解析函数，避免把解析逻辑直接堆进 `ParseLLMResponse()` 主体。
- 新增解析函数必须是纯函数风格：给定 `ChatResponse` 或纯文本输入，稳定输出工具调用、文本和错误，不依赖外部状态。
- 现有 `ChatResponse`、`LLMClient`、`PromptBuilder` 的公开行为应尽量保持不变；本次不为了 fallback 扩散修改接口。
- 若需要 provider 层配合，只允许做响应适配级别的小改动，不允许把每个 provider 都做成一套不同规则的 fallback。
- 错误处理必须遵循当前策略：原生解析失败且没有可保留文本时，才返回错误；fallback 解析失败但文本可保留时，不返回致命错误。
- 若本轮顺手引入能力注册表，其首版也必须保持最小范围：只做能力声明与查询，不耦合请求执行、工具注册或会话状态。

## Definition Of Done

- `spec.md` 中定义的 fallback 入口和边界在代码中有明确落点。
- OpenAI-compatible 原生 `tool_calls` 的既有行为保持不变。
- Ollama 或其他无原生 function calling 的文本响应，在命中 fallback 时可恢复为合法 `ToolCalls`。
- fallback 未命中时，用户仍能收到普通文本，不出现“模型未输出有效结果”的误报。
- `finish` 截断、reasoning 搬运、混合文本加工具调用等现有 Agent 语义不被破坏。
- 若新增能力注册表，代码中存在明确、单一的能力查询入口，可用于决定是否尝试原生 function calling。

## Test Contract

- 必须补充或更新单元测试，且优先放在 `agent_test.go`。
- 最低必须覆盖以下场景：原生 `ToolCalls` 存在时 fallback 不介入；OpenAI-compatible 返回纯文本伪工具调用时可恢复出工具调用；Ollama 返回纯文本伪工具调用时可恢复出工具调用；混合文本加工具调用时文本与工具调用都能保留；参数 JSON 非法或工具名不明确时降级为普通文本、不触发工具；`finish` 在 fallback 产出的调用序列中仍能正确截断。
- 测试以 mock / 构造响应对象为主，不依赖真实 LLM 服务。
- 除非已有近邻测试模式强依赖，否则不要新增 live test。
- 如果本轮引入能力注册表，还必须覆盖：支持原生 function calling 的模型走原生主路径；不支持的模型不误走原生路径；声明支持但响应缺失原生 `ToolCalls` 时仍可进入 fallback。
