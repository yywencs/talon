# OpenTalon

OpenTalon 是一个基于 Go 构建的本地 CLI Coding Agent Runtime。

它不是简单的 LLM + Tool Demo，而是一个面向真实工程场景的 Agent 执行系统。

重点关注这些问题：

- 工具调用的确定性执行
- 本地文件与命令操作的安全边界
- 状态持久化与可恢复执行
- 全链路 tracing 与 payload observability
- 多模型 Provider 的统一抽象

目标是让 Coding Agent 从“能调用工具”，走向“可控、可追踪、可回滚、可恢复”。

## 如何使用

1. 在项目根目录复制配置文件：

```bash
cp .env.example .env
```

2. 按需修改 `.env` 中的模型参数

3. 运行交互模式：

```bash
go run .
```

4. 运行单次任务：

```bash
go run . "帮我看看当前目录下有哪些 Go 文件"
```

## 目前进度

### 已完成

- ✅ CLI 运行入口，支持单次任务执行和交互式 REPL，已经可以直接作为本地命令行 Agent 使用
- ✅ Agent 主循环，能够完成“用户输入 -> 模型决策 -> 工具执行 -> observation 回流 -> 继续决策”的基本闭环
- ✅ 多模型 Provider 接入，统一兼容 Ollama 原生 `/api/chat` 和 OpenAI-compatible `/chat/completions` 两类协议
- ✅ 流式输出链路，模型生成内容可以实时输出到终端，而不是等到整段完成后一次性返回
- ✅ function calling / tool calling，能够把模型返回的结构化调用解析为本地工具动作
- ✅ 基础工具体系，已经接入 bash、文件编辑、finish 等工具，具备执行本地操作和结束任务的基础能力
- ✅ Session / Event / Observation 抽象，已经有多轮执行所需的状态、事件和观察结果建模
- ✅ tracing 与 payload observability，能够记录关键调用链路、payload 摘要和 artifact，方便调试与排障
- ✅ LLM HTTP 封装重构，已将过长的 provider 兼容层按职责拆分，降低维护复杂度并为后续扩展留出空间

### 待完成

- ❌ 严格的沙箱执行环境
- ❌ 更完整的权限控制
- ❌ 工作目录隔离和资源限制
- ❌ 更复杂的任务编排和恢复能力

## 下一步：沙箱功能

当前重点推进的是 Sandbox Runtime。

目前 terminal tool 虽然已经支持命令执行，
但本质上仍然直接运行在宿主机环境中，
缺少隔离、权限边界和可恢复控制。

下一阶段的目标是：

让 Tool Execution 从“直接执行命令”，
演进为“具备隔离、权限控制、审计与恢复能力的可控执行系统”。

计划优先完成：

- 工作目录隔离与可读写范围限制
- 命令超时控制与输出大小限制
- 危险命令识别与执行拦截
- 统一 Sandbox 抽象层，供 Tool 统一接入
- 为后续资源限制（CPU / Memory / Process）预留扩展能力
