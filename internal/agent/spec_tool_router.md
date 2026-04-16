# Agent Tool Router Specification

## 1. 基础信息

| 字段 | 值 |
|------|-----|
| **名称** | Agent Tool Router |
| **核心目的** | 将 LLM 返回的工具调用解析为具体 Action，并支持并发执行 |
| **架构位置** | `/internal/agent` 包内 |
| **设计模式** | Map-based 路由 + 并发执行池 |

---

## 2. 数据结构设计

### 2.1 ToolRouter 结构体

```go
type ToolRouter struct {
    mu        sync.RWMutex
    registry  map[string]tool.ToolFactory  // 工具名 → 工厂函数
    maxPar    int                           // 最大并发数，默认 10
}
```

### 2.2 工具调用解析结果

```go
type ParsedTools struct {
    Tools []ToolCall `json:"tools"`
    PlainMessage string `json:"plain_message,omitempty"`  // 无工具时的纯文本
}

type ToolCall struct {
    ID        string          `json:"id"`         // 用于结果对齐
    Name      string          `json:"name"`       // 工具名，如 "bash"
    Arguments json.RawMessage `json:"arguments"`  // 原始 JSON 参数
}
```

### 2.3 并发执行结果

```go
type ExecutionResult struct {
    Results []ExecutionItem `json:"results"`
    Errors  []ExecutionError `json:"errors,omitempty"`
}

type ExecutionItem struct {
    ToolName string `json:"tool_name"`
    Success  bool   `json:"success"`
    Output   string `json:"output,omitempty"`
    ExitCode int    `json:"exit_code,omitempty"`
}

type ExecutionError struct {
    ToolName string `json:"tool_name"`
    Error    string `json:"error"`
}
```

---

## 3. 核心行为与并发策略

### 3.1 主流程（step 逻辑）

```
开始 step()
    ↓
有没有待执行动作？ → 有 → 执行 → 结束
    ↓
用户消息被拦截？ → 是 → 结束
    ↓
构建 LLM 消息
    ↓
调用 LLM
    ↓
处理异常（格式错/超限/历史错误）
    ↓
解析 LLM 返回：
    ├─ 有工具调用 → 生成 Action → 并发执行
    └─ 无工具调用 → 发消息给用户 → 结束
    ↓
空回复？ → 发送纠正提示
    ↓
结束 step()
```

### 3.2 并发执行策略

| 策略 | 实现细节 |
|------|----------|
| **最大并发数** | 默认 10，可通过 `WithMaxParallelism(n)` 配置 |
| **执行池** | 使用 `errgroup.Group` + `semaphore.Weighted` 控制并发 |
| **超时控制** | 每个工具调用独立 context，超时由调用方在入口处设置 |
| **错误隔离** | 单个工具失败不影响其他工具执行 |
| **结果收集** | 所有结果完成后统一返回，包含成功/失败明细 |
"注意：如果使用 errgroup，请勿因单一工具执行失败而返回 error 导致 context 被意外 cancel。所有工具的成功/失败结果（Observation）应作为正常数据被收集聚合，仅在系统级致命错误时才中断。"

### 3.3 工具路由规则

| LLM 返回场景 | 解析方式 | 执行动作 |
| --- | --- | --- |
| 原生 Tool Calls | 直接读取 LLM 响应体中的 `resp.ToolCalls` 数组 | 提取 ID、Name、Arguments，并发抛给注册表执行 |
| 纯文本消息 | LLM 响应中 `ToolCalls` 为空，且有 `Content` | 终止 step 循环，将文本流式推给前端 |
| 混合内容 | `Content` 有值，且 `ToolCalls` 不为空 | 先将文本推给前端，然后并发执行 Tool Calls |

---

## 4. 必须拦截或处理的异常边界

| 边界场景 | 处理方式 | 返回内容 |
|----------|----------|----------|
| LLM 返回格式错误（非 XML/JSON） | 记录警告，当作纯文本处理 | MessageAction 发送原文 |
| 工具名未注册 | 记录错误，跳过该工具 | ExecutionError 包含 "unknown tool: xxx" |
| 工具参数 JSON 解析失败 | 记录错误，跳过该工具 | ExecutionError 包含 "invalid arguments" |
| 工具执行 panic | recover 住，记录错误 | ExecutionError 包含 "panic recovered" |
| 并发数超限 | 排队等待，不拒绝请求 | 顺序执行，保证最终完成 |
| context 被取消 | 立即终止所有 pending 工具 | 返回已完成的 partial 结果 |
| 单个工具超时 | 该工具返回 timeout 错误 | 其他工具继续执行 |

---

## 5. TDD 测试用例规划

### 5.1 Happy Path

| Case 名称 | 输入 | 预期结果 |
|-----------|------|----------|
| `TestRouter_SingleBashTool` | `echo hi` | 成功执行 bash，返回输出 |
| `TestRouter_MultipleTools` | 两个 bash 命令 | 并发执行，都成功 |
| `TestRouter_PlainMessage` | 纯文本 | 生成 MessageAction，不执行工具 |
| `TestRouter_MixedContent` | 文本 + 工具 | 先执行工具，再发送消息 |

### 5.2 Edge Cases

| Case 名称 | 输入 | 预期结果 |
|-----------|------|----------|
| `TestRouter_UnknownTool` |  跳过未知工具，记录错误 |
| `TestRouter_InvalidJSON` | 跳过该工具，记录解析错误 |
| `TestRouter_EmptyToolName` | 跳过空工具名 |
| `TestRouter_NestedTags` | 只解析外层，内层当作参数 |

### 5.3 并发与生命周期测试

| Case 名称 | 场景描述 | 预期结果 |
|-----------|----------|----------|
| `TestRouter_ConcurrentLimit` | 20 个工具并发，限制 5 | 最多 5 个同时执行，其余排队 |
| `TestRouter_ContextCancellation` | 执行中 context 被取消 | 立即终止所有 pending 工具 |
| `TestRouter_PartialTimeout` | 部分工具超时，部分正常 | 超时工具报错，正常工具完成 |
| `TestRouter_ConcurrentSafety` | 高并发重复注册/调用 | 无数据竞争，map 读写安全 |

### 5.4 集成测试

| Case 名称 | 测试范围 | 预期结果 |
|-----------|----------|----------|
| `TestAgentStep_WithTools` | 完整 step() 流程带工具 | LLM → 解析 → 路由 → 执行 → 返回 |
| `TestAgentStep_WithoutTools` | 完整 step() 流程无工具 | LLM → 直接消息 → 返回 |

---

## 6. 实现约束

- **CRITICAL RULE**: 所有具体工具必须通过 `BaseTool[ActionType, Observation]{}` 创建，提供 `Executor` 闭包
- **并发安全**: 工具注册表使用 `sync.RWMutex`，支持并发读取
- **错误隔离**: 单个工具失败不影响整体流程，错误信息详细记录
- **超时传播**: context 超时从 Agent 入口透传到每个工具执行
- **资源清理**: 所有 goroutine 在 context 取消时正确退出，无泄漏
- **测试覆盖**: 必须包含并发测试、边界测试、mock 测试（避免真实 LLM 调用）