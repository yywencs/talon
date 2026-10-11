# incident-v3-h09 — 池损坏两周期修复，乐观锁钉住权威状态重读

## 1. 场景目标

考察两周期连接修复中的**状态新鲜度**：第一轮软重开把池代次从 20 提到 21（部分
恢复 0.88）；第二轮重建必须携带**重读后的** `expected_pool_generation=21`——沿用
初始读数 20 会触发乐观锁冲突（"connection pool generation changed"）。期望直接把
正确参数钉进 required，度量"是否重读了权威状态"。

## 2. 主要考察机制

- **主机制**：修复前置条件/参数失效（`expected_pool_generation` 乐观锁）→ 重新
  读取权威状态。
- 次要机制：部分有效修复（0.60→0.88，未达标）+ 失败探测揭示损坏源。

## 3. 世界状态时间线（虚拟时间，tick=1m）

| 时刻 | 事件 | 世界效果 |
| --- | --- | --- |
| 5m | 内存压力损坏池共享内存段（地址解析正常） | sr 0.60；connection_refused；`pool_corruption_detected`；route-a 降权 10 |
| 5m | Agent 启动，调查 | 读到 pool=20 |
| ~5→6m | refresh_provider_connection(expected=20) | 池 21；**同一损坏段重建** → sr 仅 0.88 |
| 6m+ | 探测：hard_stop（0.88 不达标），揭示 `shared_segment_corruption_persists` | needs_agent |
| 之后 | 重读连接元数据（pool=21）→ recreate(**expected=21**；若带旧值 20 → 冲突拒绝) | 池 22（全新段）；sr 0.99 |
| 之后 | 复探 healthy → 恢复 → 基线权重 | resolved |

## 4. Agent 可见证据

- 日志：`connection_refused` + `pool_corruption_detected`（初始）；
  `shared_segment_corruption_persists`（探测揭示）。
- 连接元数据：pool 20→21→22、resolver 6→6→7——pool 变化是重读必要性的直接证据。
- Trace/Provider：peer==endpoint_ip（203.0.113.150，地址无罪，判别非 DNS 问题）。
- 指标：0.60 → 0.88 → 0.99（部分恢复信号）。

## 5. 隐藏真值

`internal_cause: memory_pressure_pool_corruption`。refresh 失败的原因（同一共享段
重建）只出现在揭示日志；期望要求引用它作为二次修复的证据基础。

## 6. 合理的成功路径

调查 → refresh(expected=20) → 探测 hard_stop + 揭示 → needs_agent 重读连接元数据
（pool=21）→ recreate(expected=21) → 复探 healthy → 恢复 → resolved。2 周期。

## 7. 合法替代路径

- 带 stale expected=20 提交 recreate → **冲突拒绝**（rejected，不计周期）→ 重读
  → 以 21 重提成功：该分支同样判过（最终成功参数一致），冲突是"世界对旧读数的
  诚实反馈"。
- 重读时引用前后两次连接快照 → 派生 `connection.pool_generation_changed`。

## 8. 必须失败或禁止的路径

- 重复 refresh（无新证据）：池已在 21，重复软重开不解决损坏源，违反
  `repeat_refresh_without_new_evidence`。
- 不重读、反复以 expected=20 提交 recreate：恒冲突，死循环违规。
- 只看 0.88 部分恢复就恢复流量：探测 require 0.98 拦截（世界诚实）。

## 9. Probe 与 World 的绑定依据

- 故障页①（无 when，0.60）：初始世界值。
- 故障页②（`after_world_effect: refresh`，0.88 + 揭示）：refresh 落地后的真实
  世界值——**部分恢复被如实表达**，不是"完全无效"。
- 健康页（`after_world_effect: recreate`）：只有全新段重建落地才解锁。
- 修复未落地时重复探测恒 hard_stop。

## 10. required_evidence 可达性表

| Evidence ID | 产生工具 | 世界来源 | 可见时点 | 回合 |
| --- | --- | --- | --- | --- |
| metric.connection_error_rate_by_route | query_metrics | 5m 事件 | 5m | 1 |
| log.connection_refused | query_logs | 5m 事件 | 5m | 1 |
| log.pool_corruption_detected | query_logs | 5m 事件 | 5m | 1 |
| trace.peer_address_observed | query_traces | 5m 事件 | 5m | 1 |
| connection.resolver_cache_generation | get_connection_metadata | 初始 | 0m | 1 |
| log.shared_segment_corruption_persists（失败探测后） | query_logs | 探测揭示 | 首个 hard_stop 后 | 2 |
| connection.pool_generation_changed（失败探测后） | get_connection_metadata ×2 引用 | 池 20→21 | 两轮引用 | 1+2 |

## 11. 为什么不是 v2 表面变体

与 v2 三条连接场景逐一区分：
- v2 connection-recovery（DNS 陈旧、refresh 全无效 0.40 不变）：h09 refresh
  **部分有效**（0.60→0.88），根因从解析层换到内存段损坏。
- v2 connection-stale-sessions（会话失效、resolver gen 变化）：h09 的 resolver
  代次在 refresh 后**不变**（6→6），损坏源在共享段而非会话。
- v2 budget（两连修全败后耗尽预算升级）：h09 两连修后成功 resolved。
关键增量：`expected_pool_generation: 21` 钉进 required——v2 从未用动作参数度量
"重读权威状态"。

## 12. 当前 DSL / Evaluator 已知限制

- 冲突分支依赖 Agent 使用旧读数，非强制路径（先重读则无冲突）；期望只钉最终
  成功参数，无法区分两种正确子路径。
- 池代次变化的"权威来源"只有 get_connection_metadata；若模型跳过重读而凭记忆
  猜中 21（如从探测揭示日志推断），也会通过——语义等价，可接受。
