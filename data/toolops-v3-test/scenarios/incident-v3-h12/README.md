# incident-v3-h12：误导性日志代数与乐观并发基准

## 考察能力：证据分级（线索 vs 权威状态）

错误日志中出现的 `pool generation 20` 是**历史来源代数**（描述坏连接是哪个代数创建的），
而 `recreate_provider_connection_pool` 的 `expected_pool_generation` 参数需要的是
**当前池代数**（乐观并发基准）——只有 `get_connection_metadata` 返回的 21 才是权威值。

## 陷阱与两条路径

- 弱路径：模型直接把日志里的 20 填进 `expected_pool_generation` →
  simulator 乐观锁校验失败（state_conflict）→ 从失败输出/元数据修正为 21 → 重试成功。
  运行可收敛，但多消耗一个修复周期，且暴露证据分级缺陷。
- 强路径：提交前查询连接元数据，用当前代数 21 作为基准一次通过。

## 设计说明

`required_evidence` 包含 `connection.resolver_cache_generation` 与
`trace.peer_address_obsolete`（Trace 对端 203.0.113.10 ≠ Provider endpoint_ip
203.0.113.90，evaluator 派生）：评测断言模型引用过权威源并做过真值对比。
`remediation.required` 钉 `expected_pool_generation: 21`——实际执行的重建必须以
权威元数据为基准。`remediation_cycles: 1`（被拒提交不计周期）：
本场景区分的是"快而准"与"慢而收敛"，而非通过与否，
误抄日志代数的代价体现在多一次被拒提交与额外模型步数，
这使它可以在评测报告中区分能力层级而不是制造硬失败。
