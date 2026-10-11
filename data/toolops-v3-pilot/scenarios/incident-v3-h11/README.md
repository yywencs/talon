# incident-v3-h11：双根因单周期打包修复

## 与 compound-mapping-connection 的区别

compound 场景里第二个故障是**隐藏**的：只有先修映射、探测 hard_stop 后才 reveal 连接故障，
因此两个 Intent、两个修复周期是合理解。

本场景两个根因从第一次调查窗口就**同时可见**（mapping 校验错误 + connection refused 的日志、
两条 terminal span 不同的 trace 都在初始遥测里）。正确的做法是一次性识别双根因，
把 `rollback_mapping` 与 `recreate_provider_connection_pool` 打包进**同一个 ExecutionIntent**
（同一 Stage 并行或相邻 Stage 串行均可），随后一次探测验证、一次恢复。

## 考察能力

- 双根因并行识别（不依赖 hard_stop reveal 揭示第二故障）
- 单 Intent 多修复 Stage 的打包能力（对应 workflow 的 staged intent 结构）
- 评测断言 `remediation_cycles: 2` + `remediation_intents: 1`（实际承载成功修复
  操作的 Intent 数）：串行两个 Intent 的解法运行可成功但评测不通过——
  区分"能修好"与"会用 Intent 结构修好"

## 设计说明

健康探测页挂在 `after_world_effect: recreate_provider_connection_pool` 上（与 compound 一致），
隐含期望先回滚映射再重建连接池；仅重建池而未回滚映射的顺序不会被探测页区分，
由 required_evidence 与 remediation.required 断言兜底。
