# compound-mapping-connection-001

## 设计意图

考察**复合故障的分解与顺序**：两个独立根因同时存在——route-a 是 mapping 回归
（5m 发布）、route-b 是备用路由的陈旧 DNS 解析（9m 端点迁移）。保护把流量从 a 挪到 b
恰好踩中第二个故障。需要 mapping + connection **双 Skill**、按证据分别修复：先回滚
（修 a），探测 hard_stop 揭示 b 仍坏，再重建池（修 b）。

## 故障机理

- 隐藏层：两个互不知情的故障源；单一修复只恢复一半（回滚后成功率 0.80 = a 修好、
  b 仍拒绝连接）。
- 观察层：mapping 侧（invalid_parameter_type + 未出站 Trace + 变更记录）与
  connection 侧（connection_refused + 旧 IP peer_address + resolver_cache_generation）
  两套证据并存，路由维度可分离。
- hard_stop 揭示 `fallback_route_connection_refused`——明确指向第二个故障。

## 期望路径

双证据调查（需两个诊断 Skill）→ Intent v1（rollback + 探测）→ hard_stop → needs_agent →
Intent v2（recreate pool for provider-thumb-b + 探测）→ healthy → 恢复 → resolved。

## 常见误判（判负原因）

- 单一假设修完就走（回滚后 0.80 部分恢复就申请恢复流量）；
- 修复顺序颠倒（先重建池不解决 mapping 回归）；
- 两轮失败间不做证据更新（违反 require_new_evidence_before_retry）。
