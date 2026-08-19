# Talon Evaluator

Talon Evaluator 对版本化的 ToolOps 运行数据执行离线确定性评分。它只读取 JSON，
不连接 Talon 数据库，也不参与 Agent 运行。

## 输入

当前支持 `talon.evaluation-input/v1`：

```json
{
  "schema_version": "talon.evaluation-input/v1",
  "artifact": {"schema_version": "talon.run-artifact/v3"},
  "expectations": {
    "schema_version": "toolops-expectation/v1.1",
    "scenario_id": "mapping-regression-rollback-001"
  }
}
```

`artifact.scenario_id` 必须与 `expectations.scenario_id` 相同。Go Exporter 按代码版本和
数据集版本从 PostgreSQL 批量读取 Artifact，加载对应 expectations，并为
每个 `run_id` 生成一份输入文件。导出方式见项目根目录 `README.md`。

## 运行

不需要第三方 Python 依赖：

```bash
PYTHONPATH=evaluator/src python3 -m talon_evaluator input.json --pretty
```

退出码：`0` 表示没有确定性失败，`1` 表示至少一项失败，`2` 表示输入或命令错误。

也可以直接传入 Go Exporter 生成的目录：

```bash
PYTHONPATH=evaluator/src python3 -m talon_evaluator evaluation-data/batch \
  --output evaluation-data/batch-result.json --pretty
```

目录模式会根据 `manifest.json` 评测全部 `completed/failed` 运行，并输出
`talon.evaluation-batch-result/v1`。`average_steps` 表示 Artifact 中的平均 Model Call 数，
`average_duration_ms` 表示 Talon 整次运行的平均墙钟耗时。运行期 `failed` 会产生
`run.completed` 失败检查，不会被成功运行隐藏。

运行测试：

```bash
PYTHONPATH=evaluator/src python3 -m unittest discover -s evaluator/tests -v
```

## LLM Judge

确定性规则无法判断的 `diagnosis.root_cause_correctness` 可以通过版本化 LLM Judge
完成。正式评测建议使用与被测 Agent 不同且能力相当或更强的模型，并固定 Judge
模型和 Prompt 版本，以便跨代码版本比较。

配置：

```dotenv
JUDGE_PROVIDER=openai-compatible
JUDGE_MODEL=your-independent-judge-model
JUDGE_ENDPOINT=http://localhost:11434/v1
JUDGE_API_KEY=
JUDGE_PASS_THRESHOLD=0.8
JUDGE_TIMEOUT_SECONDS=120
```

执行完整评测：

```bash
make eval-full \
  EVAL_INPUT=evaluation-data/batch \
  EVAL_OUTPUT=evaluation-data/batch-full-result.json
```

不带 `--judge` 的原命令仍然只运行确定性规则。Judge 返回值会替换原先的语义
`skipped` 检查，并在结果的 `judge` 字段记录模型、Judge/Prompt 版本、Prompt digest、
阈值、Token 和耗时。模型请求失败或输出不符合 JSON 契约时命令以基础设施错误退出，
不会把它误算成 Agent 失败。结果还会记录 `agent_model` 和 `same_as_agent_model`，用于
识别同模型自评；同模型不会被禁止，便于本地调试，但不建议用于正式基线。

Judge Prompt 与 Agent Prompt 采用同一套版本化惯例，位于
`src/talon_evaluator/prompts/root-cause/`（当前 `v2`），每个版本一个不可变目录
（`system.md` 正文 + `manifest.json` 版本声明），随评测器包一起分发，不依赖 Go 侧
的 `prompts/` 目录。`v2` 在 v1 基础上引入评分档位锚点、证据引用否决、关键词堆砌
惩罚、长度中立声明与边界示例，使判定从单条指令演进为结构化 Rubric；`v1` 原样保留
供历史结果对账，跨版本比较 Judge 结果时应按 `prompt_version` 分组。每次评测按
Prompt 正文计算 SHA-256 digest 并写入结果，可检测已发布 Prompt 是否被意外修改；
需要继续调整时复制为新版本目录并更新 manifest 中的 ID。

## 结果语义

- `passed`：Artifact 中存在足够事实并满足 expectation。
- `failed`：Artifact 中存在足够事实且违反 expectation。
- `skipped`：当前 Artifact 缺少稳定语义，不能可靠判断。

只要存在 `failed`，总体 verdict 为 `failed`；没有失败但存在 `skipped` 时为
`incomplete`；全部规则均可判断且通过时才是 `passed`。`score` 只统计已评规则，
`coverage` 表示可评规则占全部规则的比例。

Evaluator `0.5.0` 已确定性检查：

- Execution Intent 或 escalation 所引用查询调用的 canonical Evidence ID 是否覆盖 expectations；
- 失败探针后的新增证据及连接快照变化；
- escalation 的结构化 handoff 必填字段；
- RunArtifact 的结构化 Experience 字段完整性；
- 动态 `Execution Intent.stages[].actions` 中的修复参数和 probe `policy_id`，同时兼容 v2 Artifact 的旧 `plans` 结构；
- 修复、探针、恢复、升级和禁止操作等原有规则。

Controller 的异常发现时限、流量保护和熔断不再属于 Agent 评测范围，因此不会生成
对应检查项。当前只保留自由文本根因的语义正确性为 `skipped`；后续应交给版本化
LLM Judge，不能用脆弱的字符串相似度伪造确定性评分。旧 Artifact 没有新增结构化
字段时仍会依据 Artifact 的 `capabilities` 保守地标记为 `skipped`，不会误判为失败。
