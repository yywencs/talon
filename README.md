# Talon ToolOps Agent

Talon 当前实现了从异常调查、Plan 校验、受控修复、小流量探测到逐级恢复的 ToolOps Agent 闭环。

## 运行完整 ToolOps 链路

先在项目根目录准备 `.env`，至少配置支持 Tool Calling 的模型：

```dotenv
LLM_PROVIDER=openai-compatible
LLM_MODEL=deepseek-v4-flash
LLM_ENDPOINT=https://api.deepseek.com/v1
LLM_API_KEY=your-api-key
```

然后运行：

```bash
go run ./cmd/talon
```

默认运行 `mapping-regression-rollback-001`，使用临时 SQLite，并在终端输出 Agent 回答、审批、异步 Operation、Workflow 状态流转和最终路由权重。中风险修复会显示 `SIMULATOR AUTO-APPROVE`，表示场景运行器在隔离环境中自动批准；如需停在审批门禁：

```bash
go run ./cmd/talon --auto-approve=false
```

选择其他场景或启用 CozeLoop 时，可以继续使用 `.env` 中的 `COZELOOP_*` 配置：

```bash
go run ./cmd/talon \
  --scenario connection-recovery-two-cycles-001 \
  --timeout 5m
```

查看全部参数：

```bash
go run ./cmd/talon --help
```
