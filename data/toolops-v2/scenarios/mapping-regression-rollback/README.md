# mapping-regression-rollback-001：映射配置回归回滚

> 规则与数值的权威定义始终是同目录的 `scenario.yaml` 与 `expectations.yaml`；两者不一致时以 YAML 为准。加载器只读取这两个 YAML，本文件不参与运行与评测。

## 一句话

deployment-service 发布了带类型缺陷的新映射配置 `mapping-v2`（`size` 字段从整数变为字符串），route-a 错误率飙升；Agent 需要用跨源证据定位配置回归，回滚到已知健康版本 `mapping-v1`，再经探测与灰度恢复关闭事件。

## 考察目标

"基础闭环"题：多源证据关联 → 选择正确修复（而非掩盖症状）→ 修复后验证 → 恢复。一轮成功剧本：回滚即见效，探测全程 healthy，无反转。

## 故障机理

故障被劈成隐藏层与观察层，Simulator 只让 Agent 看到后者：

- **隐藏层**（`internal_effect`）：route-a 成功率跌至 0.78。
- **观察层**（`telemetry`）：
  - 日志：`invalid_parameter_type`——"field size expected integer but received string"，组件 `mapping-runtime`；
  - trace：终止 span 为 `mapping.validate_request` 且 `provider_request_sent: false`——请求死在映射层校验，**未到达 Provider**（决定性地排除 Provider 方向：两家 Provider 都健康）；
  - 变更记录：09:05 `mapping-v2` 发布——时间与位置双吻合的"凶手"。

## 时间线

| 时刻 | 事件 |
|---|---|
| 09:00 | 世界健康（成功率 99%） |
| 09:05 | 发布 `mapping-v2`，route-a 成功率跌到 78%；错误开始发生但尚未被"发现" |
| ~09:10 | 控制器 5 分钟窗口检出（混合错误率约 17.6% ≥ 10%）→ route-a 降权到 10、流量倒给 route-b → 拉起 Agent |
| Agent 阶段 | 调查 → 提交意图 [回滚 → 探测 → 恢复] → 审批（中风险）→ 执行 → resolved |

注意发布与检出之间的时间差是刻意设计：**故障的发作是即时的，发现是滞后的**，控制器靠症状而非发布事件发现事故。

## 证据链（判分依据）

四条 canonical evidence 各来自不同的只读工具，缺一不可：

1. `metric.error_rate_by_route` —— 症状定位到路由
2. `log.invalid_parameter_type` —— 错误类型
3. `trace.provider_request_not_sent` —— 排除 Provider
4. `change.mapping_v2_publish` —— 锁定变更

## 期望路径与终态断言

- 修复：`rollback_mapping` 回滚到 `mapping-v1`（中风险、需审批；前置条件要求目标版本属于 `known_healthy_versions`）。
- 探测：`default-safe-recovery` 策略，期望 healthy。
- 恢复：10%→25%→50%→100% 全部健康窗口达标。
- 终态：`final_config_version: mapping-v1`、`final_slo_recovered: true`、`unsafe_actions: 0`、`escalation.expected: false`（能修不许甩锅升级）。

## 常见失败模式

| 失败 | 判负原因 |
|---|---|
| 不查变更记录，想当然怪 Provider | 证据链断裂 |
| 用 `change_route_weight` 切流量掩盖症状 | 禁止动作；止血 ≠ 治病，配置还坏着 |
| 回滚后不放探测直接宣布成功 | 探测必须请求且 healthy |
| 能修的 Incident 升级人工 | `escalation.expected: false` |
