# Talon ToolOps Agent

Talon 当前实现了从异常调查、Plan 校验、受控修复、小流量探测到逐级恢复的 ToolOps Agent 闭环。

## 运行完整 ToolOps 链路

先在项目根目录准备 `.env`，至少配置支持 Tool Calling 的模型：

```dotenv
LLM_PROVIDER=openai-compatible
LLM_MODEL=deepseek-v4-flash
LLM_ENDPOINT=https://api.deepseek.com/v1
LLM_API_KEY=your-api-key
DATABASE_DSN=postgres://talon:password@localhost:5432/talon?sslmode=disable
TALON_CODE_VERSION=<git-commit>
```

然后运行：

```bash
go run ./cmd/talon
```

默认运行 `mapping-regression-rollback-001`。正式运行固定使用 PostgreSQL，必须配置 `DATABASE_DSN`；`TALON_CODE_VERSION` 用于显式记录可导出的代码版本，未设置时才尝试从 Go BuildInfo 推断。SQLite 仅用于自动化测试。终端会输出 RunArtifact ID、Agent 回答、审批、异步 Operation、Workflow 状态流转和最终路由权重。中风险修复会显示 `SIMULATOR AUTO-APPROVE`，表示场景运行器在隔离环境中自动批准；如需停在审批门禁：

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

## 导出离线评测数据

按代码版本和数据集版本导出该组合下的所有终态运行，包括 `completed` 和 `failed`：

```bash
go run ./cmd/talon-export \
  --code-version <git-commit> \
  --dataset-version toolops-v1 \
  --output evaluation-data/<git-commit>-toolops-v1
```

命令固定从 `DATABASE_DSN` 指向的 PostgreSQL 读取 `talon.run-artifact/v2`。输出目录必须尚不存在；目录中每个 `run_id` 对应一份 `talon.evaluation-input/v1` JSON，`manifest.json` 记录本批次的版本、运行 outcome 和文件清单。

对整个导出目录生成批量评测报告：

```bash
PYTHONPATH=evaluator/src python3 -m talon_evaluator \
  evaluation-data/<git-commit>-toolops-v1 \
  --output evaluation-data/<git-commit>-toolops-v1-result.json \
  --pretty
```

批量报告使用 `talon.evaluation-batch-result/v1`，包含总体及各场景成功率、`completed/failed` 数量、平均模型调用步数、Token、运行耗时、失败阶段/原因分布和 score/coverage。
