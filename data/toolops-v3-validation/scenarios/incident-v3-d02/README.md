# incident-v3-d02 — 修复有效后故障在恢复期复发

## 1. 场景目标

考察 Agent 在"修复 → 探测健康 → 恢复中故障复发"的时序下，能否丢弃旧的 healthy 探测
结论、基于恢复硬停交回后的**新证据**重新调查并改用更高强度修复，而不是复用旧结论
直接再次恢复、或重复同一低强度修复。

## 2. 主要考察机制

- **主机制**：时序/复发——recovery 健康窗口失败，Controller 原子退回保护权重并把
  控制权交回（needs_agent），旧 healthy 结论失效。
- 次要机制：二次端点变更后的陈旧解析证据（peer ≠ endpoint_ip）与
  `expected_pool_generation` 重读。

## 3. 世界状态时间线（虚拟时间，tick=1m）

| 时刻 | 事件 | 世界效果 |
| --- | --- | --- |
| 5m | provider 端点变更①（rehome → 198.51.100.77；连接器缓存仍 .10） | sr 0.55；connection_refused；route-a 降权至 10（redistribute route-b） |
| 5m | Agent 启动，调查（时钟冻结） | Trace peer=.10；Provider endpoint_ip=.77 → 过时 |
| ~5→6m | refresh_provider_connection（expected_pool_generation=8） | 池 9、resolver 4、resolved .77；sr 0.99 |
| 6→10m | 探测（4 窗口）健康；**8m 端点变更②（rehome → .99；连接器现在缓存 .77）落在探测窗口内** | 世界 sr 0.45；新日志/Trace（peer .77）注入；冻结的探测画像仍报 healthy |
| 10m | request_recovery：恢复会话按**创建时刻实时快照**（0.45）在首个窗口硬停 | **退回保护权重 10**，needs_agent |
| 10m+ | 重读：peer .77 ≠ endpoint_ip .99；探测 → hard_stop，揭示 resolver_cache_hit | 新证据 |
| 之后 | recreate_provider_connection_pool（expected_pool_generation=9） | 池 10、resolver 5、resolved .99；sr 0.99 |
| 之后 | 复探 healthy → 第二次恢复（快照已 0.99，全程健康）→ 基线权重 70 | resolved |

## 4. Agent 可见证据

- 指标：connection_error_rate（route-a）0.45→0.01→0.55→0.01 的波动序列。
- 日志：两轮 `connection_refused`（拨号地址不同 .10/.77）、恢复硬停后的
  `resolver_cache_hit`（探测揭示）。
- Trace：两次 `provider.connect`，peer 分别 .10/.77；get_providers 的 endpoint_ip
  分别 .77/.99（跨工具派生"对端过时"）。
- 连接元数据：pool 8→9→10、resolver 3→4→5、resolved_ip 变迁。
- 路由状态：route-a 权重 70→10（保护）→恢复中→退回 10→最终 70。

## 5. 隐藏真值

`internal_cause: endpoint_rehome_dns_rettl` 与 `second_rehome_overloaded_node`。
第二次 rehome 的发生时刻（8m，健康探测的窗口跨度内）是 Simulator 隐藏编排——探测
窗口画像在会话创建时冻结，8m 的世界翻转不会被当前探测会话感知；Agent 只能通过
**恢复硬停 + 重读新遥测**发现复发。

## 6. 合理的成功路径

调查 → refresh(expected_pool=8) → 探测 healthy → 恢复（硬停退保护）→ needs_agent
重读（新 Trace/日志/连接元数据）→ 探测 hard_stop + 揭示 → recreate(expected_pool=9)
→ 复探 healthy → 第二次恢复 → resolved。`remediation_cycles=2`。

## 7. 合法替代路径

- 恢复硬停后先做只读复查再探测（多一轮诊断读取）：不改变终态判定
  （expectations 只约束最终探测 healthy 与修复序列）。
- 重读时同时引用两次连接快照（pool 8/9）→ `connection.pool_generation_changed`。
- 13m 后若 Agent 先重读连接再提交 recreate：直接 expected=9，无冲突分支。

## 8. 必须失败或禁止的路径

- 复用第一次 healthy 结论：恢复硬停后不重新探测直接再次 request_recovery ——
  恢复前置条件（lastProbeOutcome=healthy）在硬停后探测 hard_stop 前仍满足，
  但世界仍 0.45，第二次恢复窗口立刻再次硬停；反复恢复属无证据重复动作。
- 重复 refresh（无新证据）：refresh 已把池从 8 提到 9，再以 expected=8 提交恒冲突，
  以 expected=9 重提则违反 `repeat_refresh_without_new_evidence`。
- 第二周期跳过重读、用旧 expected_pool_generation=8 提交 recreate：冲突拒绝。

## 9. Probe 与 World 的绑定依据

- 探测四页：故障页①（无 when，世界 0.55）、健康页①（`after_world_effect:
  refresh_provider_connection`）、故障页②（`after_event` 二次 rehome cause）、
  健康页②（`after_world_effect: recreate_provider_connection_pool`）。
  按页序取最后满足页：各阶段世界状态与所选页结局一致。
- **恢复窗口画像在会话创建时从实时世界取快照**：第一次恢复创建于 10m（世界已被
  8m 事件打回 0.45），首个窗口即硬停退保护；第二次恢复创建于重建后（世界 0.99），
  全程通过。二次 rehome 必须落在健康探测的窗口跨度（6–10m，探测最早 6m 提交、
  至少 4 个 1m 窗口）内——该约束已由测试 TestV3D02RecoveryInterrupt 固定。
- 修复未落地时重复探测恒 hard_stop（when 未满足，世界未变）。

## 10. required_evidence 可达性表

| Evidence ID | 产生工具 | 世界来源 | 可见时点 | 预期调查回合 |
| --- | --- | --- | --- | --- |
| metric.connection_error_rate_by_route | query_metrics | 5m 事件（error_type=connection_refused） | 5m | 第 1 回合 |
| log.connection_refused | query_logs | 5m / 12m 事件 | 5m / 12m | 第 1 / 3 回合 |
| trace.peer_address_observed | query_traces | 5m / 12m 事件 | 5m / 12m | 第 1 / 3 回合 |
| trace.peer_address_obsolete | query_traces + get_providers（引用后派生） | peer≠endpoint_ip | 5m 起 | 第 1 回合 |
| provider.endpoint_healthy | get_providers | 初始 + 事件 provider_metadata | 0m 起 | 第 1 回合 |
| connection.resolver_cache_generation | get_connection_metadata | 初始 | 0m 起 | 第 1 回合 |
| log.resolver_cache_hit（失败探测后） | query_logs | 13m 探测 hard_stop 揭示 | 13m | 第 3 回合 |
| connection.pool_generation_changed（失败探测后） | get_connection_metadata ×2（引用两次快照） | 池 8→9 | 需引用两次读取 | 第 1+3 回合 |

## 11. 为什么不是 v2 表面变体

v2 connection-recovery-two-cycles 是"刷新无效→重建"的单调恶化；v2 没有任何场景
出现"探测已 healthy、恢复期故障复发、权重退回保护值"——d02 的第一次修复真实有效，
复发由恢复窗口内的世界事件触发，考的是旧结论失效与新证据驱动的升级修复。

## 12. 当前 DSL / Evaluator 已知限制

- **窗口画像冻结**：探测与恢复的窗口画像都在会话创建时冻结（显式 steps 或创建时
  的实时快照），窗口中途的世界事件不会改变当前会话结果。本场景把二次 rehome 钉在
  健康探测的窗口跨度内（8m ∈ [6m,10m]），让"探测报 healthy、世界已复发"的窗口
  误差有界且必然发生，再由恢复创建快照如实硬停。
- 探针剧本页不分路由；本场景故障与探测均在 route-a，route-b 仅承担 redistribute，
  无跨路由探测歧义。
- `expected_pool_generation` 冲突分支依赖 Agent 使用旧读数，非强制路径；
  expectations 只钉最终成功参数（=9）。
- expectations 未钉探测次数（避免误伤诚实的额外诊断探测），因此无法度量
  "是否重复探测"，由 forbidden 的 repeat_refresh 与恢复硬停事实间接约束。
