# Talon ToolOps Agent

Talon 当前实现了从异常调查、Plan 校验、受控修复、小流量探测到逐级恢复的 ToolOps Agent 闭环。

## 构建带版本的二进制

使用 Makefile 构建时，可以通过 `VERSION` 指定 Agent 发布版本：

```bash
make build VERSION=v1.2.0
./bin/talon --version
```

输出类似：

```text
talon-toolops-agent/v1.2.0 (commit a84f21c7d812)
```

该 Agent 版本会在运行时注入 `app.Config`，并随每个 RunArtifact 的 `provenance.code_version` 持久化。没有显式指定 `VERSION` 时，Makefile 使用当前 Git tag 或 commit 描述；`COMMIT` 默认取当前 Git commit，也可以在 CI 中覆盖：

```bash
make build VERSION=v1.2.0 COMMIT=a84f21c7d812
```

## 独立迭代 Prompt

Agent Prompt 位于 `prompts/toolops-agent`，每个已发布版本使用独立目录。例如 `v1`、`v2` 和 `v3`。版本目录包含三个文件：

- `system.md`：System Prompt，必须保留 `{{incident_id}}` 占位符。
- `default-instruction.md`：没有显式任务指令时使用的默认指令。
- `manifest.json`：版本 ID 和用途说明。

通过 `.env` 指向该目录后，修改 Prompt 无需重新构建二进制，下次启动进程即可生效：

```dotenv
LLM_PROMPTS_DIR=prompts/toolops-agent/v3
```

目录未配置时使用编译进二进制的 `v3`。每次运行都会把 manifest 中的 `prompt_version` 和根据实际 Prompt 内容自动计算的 `prompt_digest` 写入 RunArtifact。已发布目录应保持不可变；需要修改时复制为新目录、更新 manifest 的版本 ID，再通过 `LLM_PROMPTS_DIR` 切换。digest 可以识别已发布内容是否被意外修改。

## Incident 上下文快照

每次 Controller 启动新的 Agent Run 时，Talon 都会在第一次模型调用前生成
`talon.incident-context/v1`。快照只汇总当前 Workflow、Active Skills、调用预算、
此前成功只读调用的 Evidence Ref/ID、历史 Plan 和最近一次结构化失败，不复制原始工具
输出，也不读取 Simulator 隐藏状态或 expectations。

同一份快照会作为低信任状态数据放入本轮模型输入，并记录在
`RunArtifact.agent_runs[].context_snapshot`。因此后续调查能够获得结构化 handoff，离线
评测也能还原模型决策时实际看到的状态。`run_config.context_version` 和 Snapshot digest
用于版本归因与内容一致性校验。

## 运行完整 ToolOps 链路

先在项目根目录准备 `.env`，至少配置支持 Tool Calling 的模型：

```dotenv
LLM_PROVIDER=openai-compatible
LLM_MODEL=deepseek-v4-flash
LLM_ENDPOINT=https://api.deepseek.com/v1
LLM_API_KEY=your-api-key
DATABASE_DSN=postgres://talon:password@localhost:5432/talon?sslmode=disable
```

然后运行：

```bash
go run ./cmd/talon
```

默认运行 `mapping-regression-rollback-001`。正式运行固定使用 PostgreSQL，必须配置 `DATABASE_DSN`；使用 `make build` 生成的二进制会把构建版本记录为可导出的代码版本。SQLite 仅用于自动化测试。终端会输出 RunArtifact ID、Agent 回答、审批、异步 Operation、Workflow 状态流转和最终路由权重。中风险修复会显示 `SIMULATOR AUTO-APPROVE`，表示场景运行器在隔离环境中自动批准；如需停在审批门禁：

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

## 一键运行版本化 Baseline

`eval-baseline` 会从数据集自动发现全部场景，用同一份代码构建 Agent 与 Exporter，
逐场景重复运行，导出并严格校验 Artifact，最后执行确定性评测：

```bash
make eval-baseline
```

默认参数为 `toolops-v1`、每个场景 3 次、单次超时 5 分钟，并自动生成包含 UTC
时间和 Git commit 的唯一 `EVAL_VERSION`。唯一版本可以避免 PostgreSQL 中同版本的旧
Artifact 混入新批次。需要固定版本或调整矩阵时：

```bash
make eval-baseline \
  EVAL_VERSION=eval-20260818-prompt-v3 \
  EVAL_DATASET=toolops-v1 \
  EVAL_REPEAT=3
```

正式 Baseline 可同时运行独立 LLM Judge：

```bash
make eval-baseline EVAL_JUDGE=1
```

流水线会校验：

- Agent 构建版本与目标 `code_version` 一致；
- 模型调用前完成数据集、PostgreSQL 连接和目标版本无历史 Artifact 的预检；
- 每个数据集场景恰好产生 `EVAL_REPEAT` 个 completed Artifact；
- Artifact、Dataset、Code、Prompt 版本和 Prompt digest 完整；
- 当前 Artifact capabilities 没有被旧 Exporter 丢失；
- 确定性报告没有运行失败或规则失败；
- 启用 Judge 时最终报告必须达到 100% coverage，不能包含 skipped。

默认关闭 CozeLoop 上报，但可以显式传入 `COZELOOP_ENABLED=true`。产物写入
`evaluation-data/<eval-version>-<dataset>-r<repeat>`，完整报告追加
`-full-result.json`。输出路径必须不存在，流水线不会覆盖已有评测结果。
