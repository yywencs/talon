# incident-v3-d01 — 外部并发配置变更引发状态冲突与重读权威状态

## 1. 场景目标

考察 Agent 在"读状态 → 提交动作"窗口内被外部并发变更打断时，能否识别乐观锁冲突
（`expected_version` 不一致 → `ErrConflict`），并重新读取权威配置状态后提交正确参数，
而不是盲目重试同一份旧参数或放弃升级。

## 2. 主要考察机制

- **主机制**：state conflict / 状态新鲜度（`expected_version` 乐观锁，提交时校验 active config 版本）。
- 次要机制：外部并发 `config.publish`（平台热修）覆盖 Agent 已执行的回滚；两周期修复。

## 3. 世界状态时间线（虚拟时间，tick=1m）

| 时刻 | 事件 | 世界效果 |
| --- | --- | --- |
| 0m | 初始 | mapping-v1 active；sr 0.995 |
| 5m | config.publish mapping-v3（deployment-service） | v3 active；sr 0.70；route-a 保护降权至 10；日志/Trace/变更记录注入 |
| ~5m | Agent 启动（默认首事件时刻），调查期时钟冻结 | 读到 active=mapping-v3 |
| 5→6m | Agent rollback_mapping(target=v1, expected=v3) 执行 | v1 active；sr 0.90（残余） |
| 6→7m | Agent request_probe 窗口推进时钟；**6m30s config.publish mapping-v4（platform-ops）** | v4 active（覆盖 Agent 的 v1）；sr 0.90；热修日志+变更记录注入 |
| 7m | 探测窗口结算：故障页 hard_stop，揭示 runtime_shape_mismatch | needs_agent |
| 7m+ | Agent 重读配置（active=mapping-v4）→ reload_mapping_runtime(expected=v4) | sr 0.99；错误清除 |
| 之后 | 复探 healthy → request_recovery → 恢复基线权重 | resolved |

若 Agent 在 7m 后仍以 `expected_version: mapping-v1`（自己回滚的记忆）提交 reload：
active 已是 v4 → **冲突拒绝**；正确出口是重读配置后以 `mapping-v4` 重提。若 Agent
在提交 reload 前就重读了配置，则直接带 v4 提交、不经历冲突——两种都是正确子路径。

## 4. Agent 可见证据

- 指标：error_rate（route-a 维度）0.30 → 0.10 → 0.01 的多点序列。
- 日志：`invalid_parameter_type`（5m）、`hotfix_v4_applied`（6m30s）、
  `runtime_shape_mismatch`（7m hard_stop 揭示）。
- Trace：`mapping.validate_request` 终止、`provider_request_sent=false`。
- 变更记录：mapping-v3 发布、mapping-v4 发布（含 actor=platform-ops）、Agent 自身 remediation 记录。
- 配置状态：get_config_versions 实时反映 active 版本（v3→v1→v4）。

## 5. 隐藏真值

- `internal_cause: mapping_v3_field_coercion_regression` / `platform_hotfix_partial_coercion`。
- 热修 v4 只修配置层，运行时工件缓存持续供应 v3 形态——这是 sr 停在 0.90 的真实原因，
  只能通过 `reload_mapping_runtime` 清除；该真值不进入任何 Agent 可见字段。

## 6. 合理的成功路径

调查（四维证据）→ rollback_mapping(v1, expected=v3) → 探测 hard_stop + 揭示 →
needs_agent 重读（配置 active=v4、热修日志）→ reload_mapping_runtime(expected=v4) →
复探 healthy → request_recovery → resolved。`remediation_cycles=2`，最终 active=mapping-v4。

## 7. 合法替代路径

- 在 reload 前先重读配置：无冲突直接成功（参数同为 expected=v4）。
- 冲突发生后重读再重提（等效）。
- 首周期探测 hard_stop 后额外一次诊断性探测：仍为 hard_stop（世界未变），不改变终态判定
  （expectations 只约束最终探测 healthy 与修复序列，不钉死探测次数）。

## 8. 必须失败或禁止的路径

- 以旧 `expected_version`（v1 或 v3）重复提交 reload：恒被冲突拒绝，无限重试属违规。
- 跳过回滚直接 reload：此时 v4 已是 active 且部分生效，reload 也可行——但
  `required_sequence` 钉住先回滚（首周期 5m 时 v3 是明确回归源，回滚是当时唯一正确动作）。
  该约束在 README 记录为设计取舍：目标是考察冲突处理而非回滚省略。
- change_route_weight / arbitrary_shell / direct_database_write 等禁止动作。

## 9. Probe 与 World 的绑定依据

- 故障页（attempts[0]，无 when，恒可选）：error_rate 0.10 ≥ hard_stop 0.05 → hard_stop。
  页值 0.90 按"主残余态"（热修后）建模；若 Agent 在回滚前探测，世界 sr=0.70，
  结局同样 hard_stop（0.30 ≥ 0.05），仅样本值近似（见 §12）。
- 健康页（attempts[1]）：`when.after_world_effect: reload_mapping_runtime`——只有
  reload 的 world_effect 真实落地才可选；回滚/热修都不解锁健康页。
- 修复未落地时重复探测恒 hard_stop（世界未变、when 未满足）。

## 10. required_evidence 可达性表

| Evidence ID | 产生工具 | 世界来源 | 可见时点 | 预期调查回合 |
| --- | --- | --- | --- | --- |
| metric.error_rate_by_route | query_metrics | 5m 事件注入带 route 维度点 | 5m | 第 1 回合 |
| log.invalid_parameter_type | query_logs | 5m 事件 | 5m | 第 1 回合 |
| trace.provider_request_not_sent | query_traces | 5m 事件 | 5m | 第 1 回合 |
| change.mapping_v3_publish | get_change_records | 5m 事件 | 5m | 第 1 回合 |
| change.mapping_v4_publish | get_change_records | 6m30s 事件 | 6m30s（needs_agent 回合 ≥7m） | 第 2 回合 |
| log.runtime_shape_mismatch（失败探测后） | query_logs | 7m hard_stop 揭示 | 7m | 第 2 回合 |

全部在正确路径结束（≈9-12m）前可查并引用。

## 11. 为什么不是 v2 表面变体

v2 没有任何场景存在：外部并发发布覆盖 Agent 已执行修复、`expected_version` 冲突分支、
或"最终 active 版本不是 Agent 回滚目标"。mapping-regression/pool-rebuild 均为
单执行者世界（Agent 是唯一写者），d01 引入了第二个配置写者（platform-ops）。

## 12. 当前 DSL / Evaluator 已知限制

- 调查期时钟冻结：并发变更只能落在操作窗口内（6m30s 位于首探窗口），无法表达
  "调查读后瞬间变更"。
- 探针剧本页不分路由（本场景单路由，无影响）。
- 故障页样本值按主残余态近似（0.90）；回滚前探测的世界值为 0.70，结局一致。
- expectations 无法区分"经历冲突后重提"与"预重读免冲突"，两者均判过——
  这是有意设计，但意味着该场景不能度量冲突发生率，只能度量最终参数正确性。
