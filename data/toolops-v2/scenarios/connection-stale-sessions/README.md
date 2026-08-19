# connection-stale-sessions-001

## 设计意图

`connection-recovery-two-cycles-001` 的家族变体，考同一失败回路但**不同的故障语义**：
上游会话失效未通知连接器，刷新连接池只是重开池（代次 31→32），陈旧会话仍在服务直至
后台剔除——所以刷新"成功"但探测仍 hard_stop，揭示证据是 `stale_sessions_pending_eviction`。
对应真实平台"调用失败不踢连接、自愈靠 60s 维护协程"的时间窗机理。

## 故障机理

- 隐藏层：上游会话批量失效，连接器无感知；refresh 不强制淘汰旧会话。
- 观察层：`connection_refused` + Trace peer_address 指向旧 IP；hard_stop 揭示
  `stale_sessions_pending_eviction` 日志 + `connection.pool_generation_changed`。
- 判别点：刷新后代次**已变**但仍失败——不是"没刷新"，是"刷新不够"。

## 期望路径

Intent v1（refresh + 探测）→ hard_stop → needs_agent → Intent v2（recreate + 探测）→
healthy → 恢复 → resolved。

## 常见误判（判负原因）

- 无新证据重复 refresh（forbidden：`repeat_refresh_without_new_evidence`）；
- 把刷新的操作成功当作修复成功（操作 succeeded ≠ 探测 healthy）；
- hard_stop 后直接升级（应利用揭示证据走第二轮）。
