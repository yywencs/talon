# mapping-pool-rebuild-001

## 设计意图

考察"回滚配置 ≠ 修复完成"：配置回归的副作用已经固化在连接池里，单靠回滚只能部分恢复
（成功率 0.70 → 0.85），必须结合 hard_stop 揭示的池证据做第二轮重建。对应真实平台
"配置指纹变更后旧池未重建仍在服务"的机理。

## 故障机理

- 隐藏层：mapping-v3 发布后，连接器从回归配置重建了池（pool_generation 20），回滚配置
  不会自动重建该池。
- 观察层：`invalid_response_schema` 日志、`change.mapping_v3_publish` 变更记录、
  连接元数据 `resolver_cache_generation`。
- 第一轮探测 hard_stop 时揭示 `log.stale_pool_generation`——决定性新证据。

## 期望路径

调查（mapping + 连接证据）→ Intent v1（回滚 mapping-v1 + 探测）→ 探测 hard_stop →
needs_agent → Intent v2（重建池 + 探测）→ healthy → 恢复 → resolved。

## 常见误判（判负原因）

- 回滚后探测失败即升级（应利用揭示证据走第二轮）；
- 直接重建池而不先回滚配置（required_sequence 顺序错误）；
- 把部分恢复（0.85）当作成功申请恢复流量。
