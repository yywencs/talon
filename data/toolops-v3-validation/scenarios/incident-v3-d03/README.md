# incident-v3-d03 — 聚合成功率误导，需按路由下钻定位次路由故障

## 1. 场景目标

考察 Agent 面对聚合指标（整服务成功率 0.86，略低于 0.98 SLO）时，能否不满足于
服务级数字，按 route/provider 维度下钻，把故障定位到**低权重的次路由**，并识别
主路由上的延迟红鲱鱼。

## 2. 主要考察机制

- **主机制**：多 Provider 聚合指标误导 → 按 route/provider 下钻。
- 次要机制：证据冲突（route-core 高延迟 ok Trace vs route-edge 连接拒绝）；
  修复参数必须指向正确 Provider（provider-hs-b / pool 15）。

## 3. 世界状态时间线（虚拟时间，tick=1m）

| 时刻 | 事件 | 世界效果 |
| --- | --- | --- |
| 5m | provider-hs-b 端点变更（rehome → 203.0.113.90；连接器缓存仍 .60） | route-edge（权重 20）connection_refused；聚合 sr = 0.8×0.995 + 0.2×0.30 ≈ 0.86；route-edge 保护降权至 10（redistribute route-core） |
| 5m | Agent 启动，调查 | 见 §4 |
| 修复 | rebalance_provider_pool(provider-hs-b, expected_pool_generation=15) | 池 16、resolved .90、sr 0.992 |
| 之后 | 探测 healthy → 恢复 → route-edge 回基线 20 | resolved |

注意：聚合错误率 0.14 ≥ 触发阈值 0.05，但 route-core 自身 sr 正常（0.995），
仅延迟 2400ms 逼近 SLO 上限——这是给"只看主路由就动手"的 Agent 的红鲱鱼。

## 4. Agent 可见证据

- 指标：服务级 sr 0.86；按 route 维度的 error_rate 点仅 route-edge 异常。
- 日志：`connection_refused`（component=provider-connector，消息明示 edge pool）。
- Trace：route-edge `provider.connect` peer=.60；route-core `provider.response`
  ok 但 duration 2400ms（红鲱鱼）。
- Provider：两者 healthy；provider-hs-b endpoint_ip=.90。
- 连接元数据：provider-hs-b pool 15 / resolver 5 / resolved .60。

## 5. 隐藏真值

`internal_cause: edge_provider_rehome_pool_drain`。聚合数字的算术构成
（80/20 权重混合）不出现在任何单条遥测里，需 Agent 自己分维下钻。

## 6. 合理的成功路径

query_metrics（发现服务级下降）→ 按 route 维度再查指标/日志/Trace → 定位
route-edge + provider-hs-b 陈旧解析 → rebalance_provider_pool(hs-b, expected=15)
→ 探测 healthy → 恢复 → resolved。

## 7. 合法替代路径

- 先看日志/Trace 再回到指标下钻，顺序不同、结论相同即判过。
- 修复前 dry-run（无 world_effect，不占 when 谓词）。

## 8. 必须失败或禁止的路径

- 只看服务级指标就升级（"找不到根因"）——四维证据齐全，不构成遥测缺失。
- 对 route-core / provider-hs-a 做任何修复（对象错误，`required` 参数钉死 hs-b）。
- 依据 route-core 延迟红鲱鱼做"延迟类"处置——required 工具与参数不匹配即失败。
- change_route_weight / arbitrary_shell / direct_database_write。

## 9. Probe 与 World 的绑定依据

- 故障页（无 when，世界 0.86）：error_rate 0.14 ≥ 0.05 → hard_stop。
- 健康页：`after_world_effect: rebalance_provider_pool`——只有对 hs-b 的重排
  真实落地才解锁；修错对象（如对 hs-a 重排，若存在该参数化）不解锁健康页。
- 修复未落地时重复探测恒 hard_stop。

## 10. required_evidence 可达性表

| Evidence ID | 产生工具 | 世界来源 | 可见时点 | 回合 |
| --- | --- | --- | --- | --- |
| metric.error_rate_by_route | query_metrics（route 维度点） | 5m 事件 | 5m | 1 |
| log.connection_refused | query_logs | 5m 事件 | 5m | 1 |
| trace.peer_address_obsolete | query_traces + get_providers 引用派生 | peer .60 ≠ endpoint_ip .90 | 5m | 1 |
| provider.endpoint_healthy | get_providers | 初始 + 事件 metadata | 0m | 1 |
| connection.resolver_cache_generation | get_connection_metadata | 初始 | 0m | 1 |

## 11. 为什么不是 v2 表面变体

v2 全部场景的检测/定位对象都是承载主要流量的 route-a（或唯一路由）；没有任何
场景把故障放在**低权重次路由**并用聚合指标掩盖，也没有 route-core 延迟红鲱鱼。
v2 compound 的 route-b 故障是显性第二故障（需要先修 route-a），d03 只有一个故障
但藏在聚合数字后面。

## 12. 当前 DSL / Evaluator 已知限制

- 探针剧本页不分路由：Agent 若错误探测 route-core 也可能命中同一故障页（世界聚合
  sr 0.86 < 0.95 阈值）。expectations 无法确定性校验被探测路由，只能通过修复参数
  与终态间接约束；README 记录该限制。
- `forbidden_hint` 字段是给人工审核者的说明，evaluator 当前不消费该键
  （非 Schema 声明的已知键不破坏加载，因为它位于 map[string]any 的 diagnosis 下）。
