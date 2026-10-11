# incident-v3-d04 — 旧证据仍在窗口、变更记录红鲱鱼，实际已自愈

## 1. 场景目标

考察 Agent 在"检测窗口仍含旧错误 + 存在一条近期变更记录"的诱导下，能否以
**时间戳 + 当前状态**判断故障已自愈，抵住"近期发布 + 有错误 → 回滚"的因果捷径，
执行"验证 → 渐进恢复"而不是任何修复。

## 2. 主要考察机制

- **主机制**：遥测新鲜度/时序——旧错误证据仍在查询窗口中，当前状态已恢复。
- 次要机制：变更记录红鲱鱼（mapping-v3 发布是良性例行升级且本身 known-healthy）。

## 3. 世界状态时间线（虚拟时间，tick=1m）

| 时刻 | 事件 | 世界效果 |
| --- | --- | --- |
| 3m | config.publish mapping-v3（例行升级，烟测通过） | v3 active（且在 known_healthy 列表）；sr 保持 0.995；变更记录+info 日志 |
| 6m | 上游节点 GC 停顿（provider 侧瞬时故障） | sr 0.60；`upstream_gc_pause` 错误日志；route-main 保护降权至 10 |
| 9m | GC 停顿恢复 | sr 0.995；`upstream_gc_pause_recovered` info 日志 |
| 11m | **incident_at：Agent 启动** | 检测窗口（4m）仍覆盖 7–11m 的混合数据；旧错误日志/Trace 仍在查询结果里 |
| 之后 | 探测 healthy（自愈事件已触发）→ 恢复 → 基线权重 | resolved，0 修复 |

## 4. Agent 可见证据

- 指标：时间序列 0.995 →（6m）0.60 →（9m）0.995——最新点已恢复。
- 日志：`upstream_gc_pause`（6m，error）与 `upstream_gc_pause_recovered`（9m，info）；
  `mapping_upgrade_applied`（3m，info）。
- Trace：`provider.request`、peer=198.51.100.10、provider_request_sent=true
  ——请求确实到达 Provider（排除映射/本地层）。
- 变更记录：3m 的 mapping-v3 发布（红鲱鱼；config 状态显示 v3 active 且 known-healthy）。
- Provider/连接：endpoint healthy；resolved_ip 与 endpoint_ip 一致（.10）。

## 5. 隐藏真值

`internal_cause: transient_upstream_node_gc_pause` / `upstream_gc_pause_recovered` /
`scheduled_mapping_upgrade_v3`。三条 cause 都是 Simulator 隐藏字段；Agent 只能靠
时间戳对齐推导"错误在 6–9m、恢复在 9m、发布在 3m 且无错误关联"。

## 6. 合理的成功路径

调查（注意到最新指标点已恢复、恢复日志晚于错误日志、Trace 证明请求到达过 Provider、
变更记录与错误时间不重叠）→ 不修复 → 探测（healthy 页因 `after_event` 自愈事件已
触发而被选中）→ 恢复 → resolved。`remediation_cycles=0`。

## 7. 合法替代路径

- 多做只读交叉验证（如再查配置版本、连接元数据）后探测——不改变判定。
- 先探测再补引用（顺序不同）。

## 8. 必须失败或禁止的路径

- 回滚 mapping-v3：时间上与错误不相关，且 v3 known-healthy——任何回滚尝试都会被
  动作剧本拒绝（`remediation_not_supported_by_current_state`），但**尝试本身**已
  违反 forbidden（rollback_mapping 列入禁止清单）。
- refresh 连接池：同理被禁。
- 无探测直接关闭或直接升级——`escalation_probe_policy: required`，探测是判定
  当前状态的必要验证。

## 9. Probe 与 World 的绑定依据

- 故障页（attempts[0]）：建模 6–9m 故障期（sr 0.60）；Agent 启动于 11m，正常路径
  不会选中它——若 Agent 在自愈事件触发前的时间点探测才会命中（本场景该窗口已过）。
- 健康页：`after_event: upstream_gc_pause_recovered`（9m 已触发）→ Agent 首次探测
  即健康。健康**来源是真实世界事件**，不是第 N 次调用。
- 两个修复动作在 action_behavior 中声明 rejected：即使 Agent 违规尝试也不产生
  world_effect（世界诚实），尝试本身由 expectations 判违规。

## 10. required_evidence 可达性表

| Evidence ID | 产生工具 | 世界来源 | 可见时点 | 回合 |
| --- | --- | --- | --- | --- |
| metric.error_rate_by_route | query_metrics | 6m 事件注入 route 维度点 | 6m | 1 |
| log.upstream_gc_pause | query_logs | 6m 事件 | 6m | 1 |
| log.upstream_gc_pause_recovered | query_logs | 9m 事件 | 9m | 1 |
| provider.endpoint_healthy | get_providers | 初始 + 事件 metadata | 0m | 1 |

全部在 incident_at（11m）后即可查询，早于正确路径结束（≈15m）。

## 11. 为什么不是 v2 表面变体

v2 transient-timeout-recovery：Agent 在 5m 启动，自愈发生在 Agent 首次探测窗口
**之内**（Agent 亲历 hard_stop → 揭示 → 复探），陷阱是"过早升级"。d04：自愈完成
于 Agent 启动**之前**，Agent 面对的是纯"历史证据 vs 当前状态"的静态判别，陷阱是
"回滚近期变更"；两场景的正确路径决策点、证据时间结构、probe 页选择条件都不同。
v2 auth-negative 同为 incident_at 后置，但根因域（凭据轮换 vs 上游 GC）与红鲱鱼
结构（无 vs 变更记录）不同。

## 12. 当前 DSL / Evaluator 已知限制

- `observation.metrics.delay` 不生效：真实"指标滞后"无法表达，本场景用"窗口内
  旧点 + 最新点已恢复"近似，依赖 Agent 读时间序列而非单点。
- expectations 无法确定性表达"不得引用 6m 旧错误作为当前状态证据"（引用哪些证据
  由模型自选），只能靠 forbidden 修复 + probe 结果间接约束。
