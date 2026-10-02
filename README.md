# Talon ToolOps Agent

Talon 当前实现了 ReAct 驱动的 ToolOps Agent 闭环：模型根据最新观察决定一个有界的 `ExecutionIntent`，Harness 完成校验、受控执行和 Checkpoint，再把结果交回模型决定下一步。

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

Agent Prompt 位于 `prompts/toolops-agent`，每个已发布版本使用独立目录。例如 `v1` 至 `v4`。版本目录包含三个文件：

- `system.md`：System Prompt，必须保留 `{{incident_id}}` 占位符。
- `default-instruction.md`：没有显式任务指令时使用的默认指令。
- `manifest.json`：版本 ID 和用途说明。

通过 `.env` 指向该目录后，修改 Prompt 无需重新构建二进制，下次启动进程即可生效：

```dotenv
LLM_PROMPTS_DIR=prompts/toolops-agent/v4
```

目录未配置时使用编译进二进制的 `v4`。每次运行都会把 manifest 中的 `prompt_version` 和根据实际 Prompt 内容自动计算的 `prompt_digest` 写入 RunArtifact。已发布目录应保持不可变；需要修改时复制为新目录、更新 manifest 的版本 ID，再通过 `LLM_PROMPTS_DIR` 切换。digest 可以识别已发布内容是否被意外修改。

## Incident 上下文快照

每次 Controller 启动新的 Agent Run 时，Talon 都会先生成初始的
`talon.incident-context/v2`；此后每次调用模型前都会重新生成最新状态栏。快照只汇总当前 Workflow、Active Skills、调用预算、
此前成功只读调用的 Evidence Ref/ID、历史 Execution Intent 和最近一次结构化失败，不复制原始工具
输出，也不读取 Simulator 隐藏状态或 expectations。

初始快照记录在 `RunArtifact.agent_runs[].context_snapshot`；每次模型调用前实际注入的
快照位于 `agent_runs[].model_calls[].context_snapshot`，并始终作为模型输入末尾的低信任
状态数据。因此后续调查能够获得结构化 handoff，离线评测也能还原每次模型决策时实际
看到的状态。`run_config.context_version` 和 Snapshot digest
用于版本归因与内容一致性校验。

Snapshot 中的 `evidence_ref` 可以原样传给只读工具 `get_evidence`，按需取回当前
Incident 已持久化的历史观察。返回内容会再次脱敏、限制为 32 KiB，并明确标记为
`untrusted_observation_data`；它只是历史数据，不能覆盖系统指令。该查询使用独立的
`recall_evidence` 动作，不会生成新的 Evidence Ref，也不会被算作获得了新证据。

## 执行失败规范化

Dry Run、Remediation、Probe、Recovery 的失败都会先转换成统一的结构化事实，记录
`stage/category/code/safe_summary/retryable/next_action` 以及关联的 Execution Intent、Action 和
Operation。Workflow 和评测器只依赖稳定字段做判断；平台原始错误只保留用于审计，
不会直接进入 Agent 上下文。无法识别的新错误统一记录为 `unclassified`，设置
`fallback=true` 并保守升级；修复结果不确定时记录为 `result_unknown/reconcile`，禁止
把未知副作用当作普通可重试错误。全部记录持久化在 RunArtifact 的 `stage_failures` 中。

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
  --timeout 15m
```

查看全部参数：

```bash
go run ./cmd/talon --help
```

## 运行检查点（保存与 Workflow 重建）

运行前需按 `docs/sql/README.md` 应用数据库迁移；已有数据库需新增执行 `006_create_run_checkpoints.up.sql`。运行时不会自动建表。

`run_checkpoints` 按 `run_id` 保存最近一次检查点，包含版本信息、运行配置、Workflow 快照和 Intent ID 前缀。目前接入三个保存位置：

| 位置 | 保存时机 | 保存失败时 |
| --- | --- | --- |
| `created` | Workflow 创建后，调查开始前 | 停止启动 |
| `intent_accepted` | 调查轮次返回、意图已冻结，预执行开始前 | 不进入预执行 |
| `awaiting_approval` | 策略要求审批时，与审批请求在同一事务提交 | 回滚审批请求，不继续执行 |

检查点使用独立的 `revision` 做条件更新，拒绝旧版本覆盖；使用相同预期版本重试同一份数据时，返回已提交记录。JSON 的 `schema_version` 用于识别数据格式，读取不支持的格式会报错。冻结动作的参数原样保存，读取时保留大整数精度。

这一阶段支持保存、读取和 Workflow 对象重建，尚未实现重启续跑入口或重启后的跨层状态对账；执行层已有租约、Operation 查询与 unknown 处理继续保留。检查点只代表上述最近一次保存位置，执行继续推进后可能落后于实际状态；终态仍查看 RunArtifact。Agent 对话和 Simulator 状态不在该快照中，不能直接把它当作完整进程恢复镜像。

应用代码可调用 `app.LoadWorkflow(ctx, database.Checkpoints(), runID)`，取得原始检查点（含 revision、运行配置、模型调用计数）和独立的 Workflow 对象。底层 `workflow.Restore(snapshot)` 校验身份、完整转换历史、计数、阶段位置及当前动作/预执行/审批的关联，再复制状态；不会重新生成 ID、重置预算或产生新事件。空切片经过 JSON 的 `omitempty` 后可能变为 nil，序列化状态保持一致。

该入口只读数据库，不创建新运行、不启动 Controller、不执行平台动作，也不自动应用更新的审批结果。必须完成审批和执行记录对账后才能接入实际续跑。

运行编排与持久化的职责如下（详见 [运行生命周期与持久化边界](docs/run-lifecycle.md)）：

- `app.Run` 准备输入、新建运行与 Workflow，再组装组件、推进运行和收尾；模拟器时钟退出后才捕获最终世界状态。
- `intentPersistenceGate` 在调查返回后同步保存已接受意图，即使调查返回错误或取消也会尝试保存；失败会阻止 DryRun。审计包装器只记录调查过程与错误。
- `runCheckpointWriter` 构造检查点、管理独立 revision 并锁定保存失败。不可变身份和配置来自 `runmeta`，动态用量只读取模型调用计数，不复制整个审计轨迹。
- `checkpoint.Data` 校验保存边界的数据及审批绑定；`storage` 在读写入口调用校验，并负责 SQL、JSONB、CAS 和审批跨表事务。
- Workflow 的 `DecisionCheckpoint` 是业务阶段判定；`WithWorkflowAudit` / `RecordWorkflow` 同步审计轨迹；`checkpoint.Store` 保存恢复所需的运行状态。三者不互相替代。

取消后的检查点和审计写入保留各自最长 5 秒的尽力保存窗口；无法保证进程被强制终止时写入完成。审计写入失败同样必须传播并停止推进。

## 导出离线评测数据

按代码版本和数据集版本导出该组合下的所有终态运行，包括 `completed` 和 `failed`：

```bash
go run ./cmd/talon-export \
  --code-version <git-commit> \
  --dataset-version toolops-v1 \
  --output evaluation-data/<git-commit>-toolops-v1
```

命令固定从 `DATABASE_DSN` 指向的 PostgreSQL 读取 `talon.run-artifact/v3`。输出目录必须尚不存在；目录中每个 `run_id` 对应一份 `talon.evaluation-input/v1` JSON，`manifest.json` 记录本批次的版本、运行 outcome 和文件清单。

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
  EVAL_VERSION=eval-20260818-prompt-v4 \
  EVAL_DATASET=toolops-v1 \
  EVAL_REPEAT=3
```

Agent 运行与 Judge 评测均支持并发：`EVAL_PARALLEL` 控制同时运行的场景数（默认 1
等价旧串行行为），`EVAL_JUDGE_CONCURRENCY` 控制 Judge 并行调用数（默认 1）。并发时
每个 Run 的输出写入 `<输出目录>-run-logs/` 独立日志，失败以标记文件计数，导出与
校验仍在全部运行结束后串行执行：

```bash
make eval-baseline EVAL_DATASET=toolops-v2 EVAL_PARALLEL=4 EVAL_JUDGE=1 EVAL_JUDGE_CONCURRENCY=4
```

并发度建议从 3-5 起步：上限受 PostgreSQL 连接数和 LLM API 限流约束，触发限流会
把模型失败误混入场景失败。

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
