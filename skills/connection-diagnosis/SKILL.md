---
name: connection-diagnosis
description: >-
  调查 DNS 解析、socket、Provider 连接和连接池状态异常。Use when 日志或 Trace 出现 connection refused、timeout、旧解析地址、连接池失效或类似网络连接证据。Don't use when 主要证据是 401/403、凭据撤销、字段类型错误、Schema 校验失败或配置映射回归；这些情况应使用 credential-diagnosis 或 mapping-diagnosis。
---

# Connection Diagnosis

定位 DNS、连接池或 Provider 连接状态异常，并使用当前观测状态生成具备并发保护的修复计划。

## 调查流程

1. 使用公共范围和指标工具确认受影响的服务、路由、Provider、错误率和故障时间窗。
2. 调用 `query_logs` 确认连接错误类型和目标 Provider。
3. 仅在需要区分 DNS、握手、连接池或上游失败时调用 `query_traces`。
4. 调用 `get_connection_metadata` 读取解析地址、连接状态、连接池代次和解析缓存代次。
5. 将日志或 Trace 的失败阶段与连接元数据相互验证，再选择刷新连接或重建连接池等可用能力。
6. 获取恢复策略并调用 `submit_plan`；把最近一次实际观察到的 generation 作为 `expected_*_generation`。

## 参数语义

- `expected_pool_generation` 表示执行前观察到的当前连接池代次，不是操作后的目标代次。
- 其他 `expected_*` 字段同样表示乐观并发校验使用的当前值，禁止自行递增。
- 状态冲突后重新读取连接元数据；不要猜测新值或更换幂等键绕过冲突。

## 证据与停止条件

- 至少保留故障指标、连接错误以及连接元数据三类证据。
- 已定位失败阶段且修复动作及其前置版本明确时，停止继续查询并提交 Plan。
- 证据否定连接假设并指向凭据或 mapping 故障时，引用新证据调用 `unload_skill`；下一轮再加载对应 Skill。若证据表明是复合故障，则保留本 Skill 并追加对应 Skill。
- 连接状态无法可靠读取、没有安全修复能力或冲突后无法获得新状态时，调用 `escalate_incident`。

## 约束

- 不查询凭据和配置版本，除非现有连接证据明确否定 connection 假设。
- 不重复读取连接元数据，除非执行、冲突或外部事件可能已经改变状态。
- 不把探测成功等同于最终状态已经恢复。
- 不直接执行修复；只通过 `submit_plan` 提交结构化动作。
