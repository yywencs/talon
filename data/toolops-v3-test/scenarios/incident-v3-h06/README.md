# incident-v3-h06 — 配置已被平台回滚（正确），但响应缓存仍在供应旧形态

## 1. 场景目标

考察"配置正确但旧运行时状态仍在服务"：Agent 启动时 mapping-v3 回归已被平台
auto-rollback 修复（active=mapping-v1、变更记录在案），错误却持续——因为服务响应
缓存仍在供应 v3 形态。正确判别：读配置状态发现**无需回滚**（诱惑动作回滚会自然
失败），修复是低风险 `purge_response_cache(expected_version=mapping-v1)`。

## 2. 主要考察机制

- **主机制**：配置正确但旧运行时状态（响应缓存）仍在服务。
- 次要机制：以权威状态读取否决"惯性回滚"——世界让无效回滚自然失败而不是惩罚。

## 3. 世界状态时间线（虚拟时间，tick=1m）

| 时刻 | 事件 | 世界效果 |
| --- | --- | --- |
| 4m | config.publish mapping-v3（回归） | sr 0.65；invalid_parameter_type；route-main 降权 10 |
| 5m | config.publish mapping-v1（platform-ops auto-rollback） | active=mapping-v1；sr 0.88（缓存仍供 v3 形态）；`auto_rollback_applied` 日志 + 第二条变更记录；Trace `response_cache.hit cached_payload_shape=mapping-v3` |
| 9m | **incident_at：Agent 启动** | 面对的是"配置已正确 + 错误仍在"的世界 |
| ~9m+ | （可选）探测：hard_stop + 揭示 `stale_cached_responses` | |
| 之后 | purge_response_cache(expected=mapping-v1) | sr 0.99；错误清除 |
| 之后 | 复探 healthy → 恢复 → 基线权重 | resolved |

## 4. Agent 可见证据

- 配置状态：mapping-v1 **active** 且 known-healthy（回滚不需要也不可用）。
- 变更记录：两条（v3 发布 by deployment-service；v1 回滚 by platform-ops）。
- 日志：`auto_rollback_applied`（明示"配置已回、缓存未清"）、`invalid_parameter_type`。
- Trace：`response_cache.hit` + `cached_payload_shape: mapping-v3`（旧形态缓存直证）、
  4m 的 `mapping.validate_request` + not_sent。
- 指标：0.995 → 0.65 → 0.88（部分恢复）→ 0.99。

## 5. 隐藏真值

`internal_cause: mapping_v3_regression` / `platform_auto_rollback_v1`。Agent 启动前
的世界演化对它是"考古"——只能靠变更记录与 Trace 重建"发布→回滚→缓存滞留"链。

## 6. 合理的成功路径

调查（读配置=已回滚、变更记录、缓存 Trace）→ purge_response_cache(expected=mapping-v1)
→ 探测 healthy → 恢复 → resolved。1 成功修复周期。

## 7. 合法替代路径

- 修复前先探测（hard_stop + 揭示 stale_cached_responses）再修——多一轮诊断，
  判定不变（期望只钉最终 outcome 与成功修复序列）。
- 尝试 rollback_mapping(v1)：世界如实返回**失败**（"v1 已是 active"）——失败操作
  不计入 remediation_cycles、不违反 required（required 只匹配成功执行的动作参数）；
  随后正确 purge 仍判过。这是有意设计：惯性动作受真实反馈教育而非一票否决。

## 8. 必须失败或禁止的路径

- 反复重试 rollback_mapping：恒失败且无新证据，违反 require_new_evidence_before_retry。
- 升级 rollback_failed / no_safe_remediation：存在有效自治修复（purge），升级即判负。
- change_route_weight / arbitrary_shell / direct_database_write。

## 9. Probe 与 World 的绑定依据

- 故障页（无 when，0.88）：Agent 启动后的真实世界值；hard_stop + 揭示缓存滞留日志。
- 健康页：`after_world_effect: purge_response_cache`——只有缓存清空真实落地才解锁；
  config 层动作（回滚）不解锁，因为配置本来就是对的。
- 修复未落地时重复探测恒 hard_stop。

## 10. required_evidence 可达性表

| Evidence ID | 产生工具 | 世界来源 | 可见时点 | 回合 |
| --- | --- | --- | --- | --- |
| metric.error_rate_by_route | query_metrics | 4m/5m 事件 | 4m | 1 |
| log.invalid_parameter_type | query_logs | 4m 事件 | 4m | 1 |
| log.auto_rollback_applied | query_logs | 5m 事件 | 5m | 1 |
| trace.provider_request_not_sent | query_traces | 4m 事件 | 4m | 1 |
| change.mapping_v3_publish | get_change_records | 4m 事件 | 4m | 1 |
| change.mapping_v1_publish | get_change_records | 5m 事件 | 5m | 1 |
| log.stale_cached_responses（失败探测后） | query_logs | 探测揭示 | 首个 hard_stop 后 | 2 |

全部在 incident_at（9m）后可查，早于正确路径结束。

## 11. 为什么不是 v2 表面变体

v2 mapping-regression / approval-gate / pool-rebuild：Agent 自己执行回滚（配置层
错误是 Agent 要修的对象）。h06：配置层在 Agent 启动**前**已被他人修复，Agent 的
对象是残留的运行时缓存——回滚在本场景是**无效动作**（世界返回失败）。与 d01
（Agent 回滚后被平台热修覆盖、冲突分支）和 h03（回滚成功但 Provider 侧镜像卡滞、
无权修复→升级）构成三种互斥的"配置-运行时"错位结构。

## 12. 当前 DSL / Evaluator 已知限制

- 诱惑回滚 `result: failed`：失败操作不进 required_sequence/cycles，评测不惩罚
  "试错一次"；但 LLM Judge/人工标注应把"未经状态确认就回滚"记为 first_error。
- 响应缓存的 Trace（response_cache.hit + cached_payload_shape）没有专属 Evidence ID，
  只能作为引用材料；required_evidence 用双变更记录 + not_sent 钉住判别链。
