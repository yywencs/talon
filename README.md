# Talon ToolOps Agent

Talon 是一个用 Go 构建的故障诊断与修复 Agent。它通过日志、指标和 Trace 调查故障，提出修复方案，并在预执行、审批和效果验证的约束下执行操作。

项目提供可复现的 Simulator 故障场景，用于开发和评测 Agent；也支持连接独立运行的 Simulator，验证进程中断后的恢复行为。

## 主要能力

- **调查与修复**：模型根据最新观察决定下一步，执行层负责校验并推进修复、探测和恢复。
- **受控执行与续跑**：保存检查点，通过租约、幂等键和结果对账控制重复操作，支持手动续跑。
- **版本化经验**：从历史运行提炼候选经验，经审核和场景验证后发布，支持检索与回滚。
- **运行审计与评测**：记录工具调用、执行结果和版本信息，支持离线评测与批量对照实验。

## 快速开始

需要 Go 1.25.5 或更高版本、PostgreSQL，以及支持 Tool Calling 的模型服务。

**1. 配置环境**

参考 [.env.example](.env.example)，在项目根目录创建 `.env`，至少填写：

```dotenv
LLM_PROVIDER=openai-compatible
LLM_MODEL=your-model
LLM_ENDPOINT=https://your-provider.example/v1
LLM_API_KEY=your-api-key
DATABASE_DSN=postgres://talon:password@localhost:5432/talon?sslmode=disable
```

**2. 初始化数据库**

首次运行前，需按版本顺序执行 PostgreSQL 数据库迁移。运行时不会自动建表。

**3. 运行一个故障场景**

```bash
go run ./cmd/talon
```

默认运行 `mapping-regression-rollback-001`，终端会输出运行 ID、调查结果和执行过程。内嵌场景默认自动批准中风险操作；需要停在审批门禁时使用 `--auto-approve=false`。

```bash
# 选择其他场景
go run ./cmd/talon --scenario connection-recovery-two-cycles-001

# 查看全部参数
go run ./cmd/talon --help
```

## 开发与测试

```bash
make build              # 构建 bin/talon
go test ./...           # Go 测试
make test-evaluator     # Python 评测器测试（需要 Python 3）
```
