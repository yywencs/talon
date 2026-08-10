# ToolOps v1 数据集

本目录保存 `toolops-v1` 格式的 ToolOps 事件数据，供 Talon Simulator、Evaluator 和 Benchmark 使用。完整格式定义见 [`docs/data-schema/README.md`](../../docs/data-schema/README.md)。后续不兼容的数据格式应建立独立的 `toolops-v2`、`toolops-v3` 目录，不直接改变本版本语义。

每条数据位于 `scenarios/<scenario-name>/`，包含两个文件：

- `scenario.yaml`：描述初始状态、控制器策略、故障时间线、Agent 可用的只读观测、受控修复函数、探测函数及动作后的虚拟世界变化。
- `expectations.yaml`：描述期望的控制器行为、诊断证据、修复与探测轨迹、恢复或升级结果，仅供 Evaluator 评分。
