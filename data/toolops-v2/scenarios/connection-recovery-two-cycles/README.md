# connection-recovery-two-cycles-001：连接恢复两个周期

> 规则与数值的权威定义始终是同目录的 `scenario.yaml` 与 `expectations.yaml`；两者不一致时以 YAML 为准。加载器只读取这两个 YAML，本文件不参与运行与评测。

## 一句话

Provider 迁移服务端点（IP 从 .10 换到 .25），而连接器进程内的 DNS 缓存仍持有旧 IP；低风险的"刷新连接池"治标不治本（池重开但缓存未清），Agent 必须探测失败、拿到失败后才解锁的新证据、修正假设，再用"重建连接池 + 清缓存"完成第二轮修复。

## 考察目标

"韧性"题：假设修正与从失败中学习。识别"操作成功 ≠ 问题解决"的假成功；失败后取证而非盲目重试；把 hard_stop 当作信息增益而非死局。

## 故障机理

```
Provider 侧（我们管不了）          平台内部（Simulator 隐藏状态）
真实 IP: 192.0.2.25（健康）        连接池 pool_generation: 12
旧 IP .10 已无人监听              DNS 缓存 resolver_cache_gen: 4 → 仍解析到 .10
                                 → 连接器拨 .10:443 被拒 → route-a 成功率 40%
```

- "夹层"故障：Provider 本身健康（`endpoint_health: healthy`）、我方变更记录为空、路由配置无人动过——三种直觉误诊方向全部被证据堵死，坏的是进程内缓存状态。
- 观察层特意暴露连接元数据（`expose_resolver_cache_generation: true`）：没有缓存代数，Agent 无法在失败后区分"刷新没执行"和"刷新执行了但缓存没清"——两段式证据链就断了。

## 修复工具是风险阶梯，顺序被平台锁死

| 工具 | 风险 | 效果 | 前置条件 |
|---|---|---|---|
| `refresh_provider_connection` | 低，免审批 | 重开现有池，**不清 DNS 缓存** | 无（首选温和方案） |
| `recreate_provider_connection_pool` | 中，需审批 | 新建池并**清缓存**（对症） | `refresh_failed_probe_required`——必须"刷新过且探测失败"才解锁 |

平台把"先温和后激进"的运维纪律编码进工具契约：不能跳阶直接动中风险方案，也不能停在第一阶宣布完事。

## 探测剧本与线索解锁

- 第一轮探测（`attempts[0]`）：成功率 0.40、`connection_refused` → 命中 hard_stop。**hard_stop 触发时世界注入决定性日志**（`reveal_on_hard_stop`）："connection pool generation 13 reused resolver cache generation 4"。
- 第二轮探测（`attempts[1]`）：全部 healthy。
- 关键设计：刷新作为 Operation 是**成功**的（池代 12→13），不探测就没有任何信号表明问题还在——**探测是揭穿"假成功"的唯一手段**，也是唯一能解锁决定性线索的途径（不探测、探测不失败都拿不到）。

## 时间线（两轮循环）

| 阶段 | 事件 |
|---|---|
| 11:05 | Provider 迁移端点，route-a 成功率跌到 40% |
| ~11:09 | 检出（混合错误率约 42% ≥ 15%）→ route-a 降到权重 10 → 拉起 Agent |
| 第一轮 | 初始假设"连接状态过期" → 刷新（操作成功但没修好）→ 探测 hard_stop → 控制器退回保护态 → needs_agent |
| 第二轮 | 新证据：解锁的日志 + 池代已变（12→13）+ 缓存代未变（4→4）→ 假设修正为 DNS 缓存 → 重建连接池（过审批）→ 探测 healthy → 恢复 → resolved |

## 证据链（分两段）

- **初始诊断**（5 条）：`metric.connection_error_rate_by_route`、`log.connection_refused`、`trace.peer_address_obsolete`、`provider.endpoint_healthy`（排除 Provider）、`connection.resolver_cache_generation`（指向缓存）。
- **失败后**（3 条，`evidence_after_failed_probe`）：`log.resolver_cache_hit`、`connection.pool_generation_changed`、`connection.resolver_cache_generation_unchanged`——共同构成"刷新无效且缓存未清"的假设修正判据。

## 期望路径与终态断言

- 修复序列（`required_sequence`）：cycle 1 = `refresh_provider_connection`，cycle 2 = `recreate_provider_connection_pool`。
- 探测序列：cycle 1 期望 **hard_stop** 且控制器退回保护态；cycle 2 期望 healthy。
- 终态：`final_slo_recovered: true`、`final_resolved_ip: 192.0.2.25`、`remediation_cycles: 2`、`unsafe_actions: 0`、`escalation.expected: false`（hard_stop 不是死局，放弃即失败）。
- Experience 七字段把科学方法存档：症状 → 初始假设 → 无效修复 → 失败后新证据 → 最终根因 → 有效修复 → 适用性。

## 常见失败模式

| 失败 | 判负原因 |
|---|---|
| 刷新后看到 Operation 成功就结案 | 假成功；终态 SLO 未恢复，探测序列也对不上 |
| hard_stop 后原样重试刷新 | `repeat_refresh_without_new_evidence` 禁止；`require_new_evidence_before_retry` |
| 跳过刷新直接重建 | 工具前置条件未满足，提交即被拒 |
| hard_stop 后直接升级人工 | `escalation.expected: false`——失败是信息增益，不是放弃理由 |

修复周期预算上限 3、要求 2：允许浪费一次，不允许浪费两次。
