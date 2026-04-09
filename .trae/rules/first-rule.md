# OpenTalon 项目规范

## 代码风格

- **注释语言**：所有注释和文档使用中文，符合 GoDoc 规范。
- **注释原则**：不主动添加注释，仅在关键逻辑处说明架构意图；代码本身应自解释。
- **命名**：遵循 Go 惯例，驼峰式，无下划线。接口名以 `er` 结尾（如 `Agent`、`LLMClient`）。
- **错误处理**：`error` 始终作为最后一个返回值传递，不丢弃。
- **包引用顺序**：标准库 → 第三方库 → 内部包，组间空行分隔。

## Context 传播

- Context 作为函数首个参数传递。
- HTTP 层使用请求自带的 `c.Request.Context()`，一路透传到最底层。
- 不在 `context.Background()` 上做业务超时——超时由调用方在入口处通过 `context.WithTimeout` 决定。
- 日志函数提供两组：`Debug`/`Info`/`Warn`/`Error`/`Fatal`（无 ctx）和 `DebugWithCtx` 等（带 ctx）,日志用 pkg/logger 包。

## 测试

- 单元测试使用 mock 避免真实 LLM 调用。
- Live test（需要真实 API key）通过环境变量 `RUN_LIVE_LLM_TESTS=1` 显式开启，默认跳过。
- 测试文件与被测代码同包并行放置，文件名加 `_test` 后缀。

## 日志

- 使用 `pkg/logger 包`下的日志函数。
- Debug 级别仅在 `DEBUG=true` 时开启。
- 生产环境写文件使用 JSONHandler，便于日志收集；开发环境使用 TextHandler 输出到 stdout。
