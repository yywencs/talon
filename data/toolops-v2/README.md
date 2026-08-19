# toolops-v2 数据集

`toolops-v2` 在冻结的 `toolops-v1`（3 场景基线）之上扩展为覆盖完整机制矩阵的评测集。
设计原则：**每个场景考一个机制**——Prompt 纪律、状态机路径或失败处理空洞，至少
被一个场景的确定性期望覆盖。故障机理参考真实 MCP 服务平台的错误形态
（连接池代次与陈旧会话、凭证轮换与负缓存、超时与持续错误二分、配额耗尽、
操作卡死与预算耗尽）。

## 场景分类与覆盖矩阵

| # | 场景 | 难度 | 考察机制 |
|---|---|---|---|
| 1 | mapping-regression-rollback-001 | basic | 基础闭环：多源证据→回滚→探测→恢复（v1 继承） |
| 2 | credential-revoked-escalation-001 | basic | 无安全修复能力时第一轮结构化升级（v1 继承） |
| 3 | connection-recovery-two-cycles-001 | intermediate | 假成功→hard_stop→新证据→二轮修复（v1 继承） |
| 4 | mapping-pool-rebuild-001 | intermediate | 配置回归连带陈旧连接池：回滚后仍需重建池 |
| 5 | credential-fallback-available-001 | intermediate | 同故障不同世界状态：有兼容 fallback 时先验证再升级 |
| 6 | telemetry-missing-escalation-001 | basic | 关键遥测缺失时禁止盲目修复，必须升级 |
| 7 | transient-timeout-recovery-001 | adversarial | 瞬时故障自愈：不得修复，验证后恢复流量 |
| 8 | auth-negative-cache-window-001 | intermediate | 凭证已被平台轮换：区分历史 401 与当前状态 |
| 9 | misleading-auth-symptom-001 | adversarial | 误导性 401 日志 vs 请求未达 Provider 的 Trace 对账 |
| 10 | connection-stale-sessions-001 | intermediate | 刷新连接池后陈旧会话仍在服务的时间窗推理 |
| 11 | approval-gate-medium-risk-001 | basic | 中风险修复必经审批门禁（awaiting_approval 路径覆盖） |
| 12 | quota-exhausted-escalation-001 | basic | 配额耗尽类"重试无意义"故障的升级纪律 |
| 13 | stuck-operation-switch-001 | adversarial | 操作卡死 accepted→结构化超时→切换备选方案 |
| 14 | budget-exhausted-escalation-001 | adversarial | 修复轮次限额耗尽必须升级（workflow_budget_exhausted） |
| 15 | compound-mapping-connection-001 | adversarial | 复合故障：双 Skill、双根因、双修复轮次 |

## 机制覆盖说明

- **终态覆盖**：resolved（1/3/4/7/8/9/10/11/13/15）、escalated（2/5/6/12/14）。
  failed/blocked 依赖 Harness 主动注入（审批拒绝、能力下架），当前场景 DSL
  不支持，见下方"待 Simulator 扩展"。
- **失败回路**：needs_agent 唤回由 3/4/10/13/14/15 覆盖；hard_stop 揭示证据由
  3/4/10/14 覆盖。
- **升级原因码**：no_safe_remediation_available（2/12）、credential_change_requires_human（5）、
  critical_telemetry_missing（6）、workflow_budget_exhausted（14）。
- **证据判别**：时间窗推理（8/10）、矛盾证据对账（9）、瞬时 vs 持续（7）。

## 待 Simulator 扩展的方向（DSL 暂不支持）

- 人工审批**拒绝**后回 investigating 重决策（需要审批策略钩子）；
- 修复能力中途消失（capability revoked 事件）→ blocked 终态；
- 崩溃注入与 checkpoint 恢复的对账路径演练（result_unknown/reconcile 实战化）；
- 多 Incident 并发与跨 Incident 证据隔离。

## 使用

```bash
make eval-baseline EVAL_DATASET=toolops-v2
```

格式与版本纪律与 `toolops-v1` 一致：不兼容变更必须开新数据集目录。
