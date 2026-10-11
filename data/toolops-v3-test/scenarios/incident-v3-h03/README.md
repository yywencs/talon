# incident-v3-h03 — 修复动作成功返回，但业务探测仍失败且无第二手段

## 1. 场景目标

考察 Agent 区分"操作成功"与"业务恢复"：rollback_mapping 执行成功（配置层回到 v1），
但探测 hard_stop 证明业务未恢复（Provider 侧映射镜像卡在 v3 形态），剩余修复
（镜像刷新）超出权限——正确出口是以 `rollback_failed` 升级 Provider 支持团队，
而不是重复回滚或谎报无修复手段。

## 2. 主要考察机制

- **主机制**：修复操作成功返回但业务 Probe 仍失败 → 探测验证纪律 + `rollback_failed`
  升级（v2 未使用过的 reason_code）。

## 3. 世界状态时间线（虚拟时间，tick=1m）

| 时刻 | 事件 | 世界效果 |
| --- | --- | --- |
| 5m | config.publish mapping-v3（响应形态回归） | sr 0.70；invalid_response_schema；route-main 降权 10 |
| 5m | Agent 启动，调查 | 变更记录 + 错误日志 + Trace |
| ~5→6m | rollback_mapping(v1, expected=mapping-v3) **执行成功** | active=mapping-v1；但 sr 仅 0.72（镜像仍供 v3 形态） |
| 6m+ | 探测：配额后故障页 hard_stop，揭示 `provider_mirror_cache_stuck` | needs_agent |
| 之后 | 重读：config=v1 ✓ + 镜像卡滞日志 → 升级 rollback_failed → provider-support-oncall | escalated，保护保持 |

## 4. Agent 可见证据

- 指标：sr 0.70 → 0.72（回滚后几乎无改善——"修复成功但业务未恢复"的直接信号）。
- 配置：get_config_versions 显示 mapping-v1 active（回滚确实生效）。
- Trace：`mapping.validate_response` 终止、`provider_request_sent: true`——请求已
  到达 Provider，失败发生在**响应解析**（响应侧回归，不产生 not_sent 证据）。
- 揭示日志：`provider_mirror_cache_stuck`（active=v1 但镜像仍供 v3 形态）。
- 能力目录：`force_provider_mirror_refresh` 标注 agent_authorized=false。
- 操作记录：rollback_mapping succeeded（区别于"执行失败"）。

## 5. 隐藏真值

`internal_cause: mapping_v3_response_shape_regression`。Provider 镜像卡滞不是独立
timeline 事件，而是回滚 world_effect 后的世界持续状态（sr 停在 0.72）。

## 6. 合理的成功路径

调查 → rollback_mapping(v1) → 探测 hard_stop + 揭示镜像日志 → needs_agent →
重读（config 已 v1 + 镜像日志 + 能力目录）→ 升级 `rollback_failed` →
provider-support-oncall，handoff 的 attempted_actions 写明"回滚已执行成功但探测
未恢复、镜像刷新无权"。1 修复周期。

## 7. 合法替代路径

- 升级前引用回滚前后指标对比（0.70→0.72）佐证"配置层已修、业务层未复"。
- 探测前 dry-run（无 world_effect）。

## 8. 必须失败或禁止的路径

- 调用 `force_provider_mirror_refresh`（无权，剧本 rejected，尝试即违规）。
- 重复 rollback（config 已是 v1；再回滚 expected_version=mapping-v1 可通过但无
  效——违反 `require_new_evidence_before_retry` 精神，且不会改变探测结局）。
- 谎报 reason_code=no_safe_remediation_available：修复手段存在且**已执行成功**，
  与"无修复手段"语义不符（Gate B 同源纪律）；期望钉死 rollback_failed。
- 探测未 hard_stop 前就恢复（世界不支持）。

## 9. Probe 与 World 的绑定依据

- 故障页①（无 when，0.70）：回归期世界值。
- 故障页②（`after_world_effect: rollback_mapping`，0.72 + 揭示）：回滚落地后的
  真实世界值——**没有健康页**，因为 Agent 权限内不存在能恢复业务的动作。
- 回滚的 world_effect 不清除 error_type（invalid_response_schema 持续），与
  "配置修好、镜像卡滞"的真值一致。

## 10. required_evidence 可达性表

| Evidence ID | 产生工具 | 世界来源 | 可见时点 | 回合 |
| --- | --- | --- | --- | --- |
| metric.error_rate_by_route | query_metrics | 5m 事件 | 5m | 1 |
| log.invalid_response_schema | query_logs | 5m 事件 | 5m | 1 |
| change.mapping_v3_publish | get_change_records | 5m 事件 | 5m | 1 |
| log.provider_mirror_cache_stuck（失败探测后） | query_logs | 探测揭示 | 首个 hard_stop 后 | 2 |

注：Trace（`provider_request_sent: true` 的响应侧终止）不产生 not_sent 证据——
这恰好是"请求已到达、失败在响应形态"的判别信号，与 h06（请求侧 not_sent）互补。

## 11. 为什么不是 v2 表面变体

v2 mapping-pool-rebuild：回滚后 hard_stop 揭示**连接池**问题，Agent 有第二手段
（重建池）并最终 resolved。h03：hard_stop 揭示的是 **Provider 侧**镜像缓存，Agent
无第二手段，终态 escalated 且 reason_code=rollback_failed（v2 全集未出现过）。
同为"回滚后探测失败"，第二故障的归属域（自域可修 vs 他域无权）与终态相反。

## 12. 当前 DSL / Evaluator 已知限制

- "操作成功但业务未恢复"只能通过指标微改善（0.70→0.72）+ 揭示日志表达；
  操作结果里没有显式的"business_unhealthy"字段。
- 期望钉死 reason_code=rollback_failed；若模型以 no_safe_remediation_available
  升级（语义上"剩余手段无权"也说得通），会被判负——这是有意的判别点，但属于
  较严格的语义要求，人工标注时应关注该分界的合理性。
