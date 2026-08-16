# Talon Evaluator

Talon Evaluator 对版本化的 ToolOps 运行数据执行离线确定性评分。它只读取 JSON，
不连接 Talon 数据库，也不参与 Agent 运行。

## 输入

当前支持 `talon.evaluation-input/v1`：

```json
{
  "schema_version": "talon.evaluation-input/v1",
  "artifact": {"schema_version": "talon.run-artifact/v2"},
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

## 结果语义

- `passed`：Artifact 中存在足够事实并满足 expectation。
- `failed`：Artifact 中存在足够事实且违反 expectation。
- `skipped`：当前 Artifact 缺少稳定语义，不能可靠判断。

只要存在 `failed`，总体 verdict 为 `failed`；没有失败但存在 `skipped` 时为
`incomplete`；全部规则均可判断且通过时才是 `passed`。`score` 只统计已评规则，
`coverage` 表示可评规则占全部规则的比例。

当前有意跳过自由文本根因等价、语义 Evidence ID 覆盖率、结构化 handoff 和
Experience 完整性。这些能力应在引入 canonical ID 或版本化 Judge 后实现，不能用
脆弱的字符串相似度伪造确定性评分。
