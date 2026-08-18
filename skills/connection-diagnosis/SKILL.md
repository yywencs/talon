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

## 分阶段修复与探测

- 把 probe 的 `operation_status=succeeded` 与业务 `output.outcome=healthy` 分开判断；
  `hard_stop` 表示探测操作成功执行但业务验证失败，禁止进入 recovery。
- 如果能力目录同时提供低风险刷新和带 `refresh_failed_probe_required` 前置条件的连接池重建，
  第一周期应提交“刷新 → probe”。首次 probe 为 `hard_stop` 时，Checkpoint 必须使用
  `needs_agent`，不要直接 `escalate`；该失败探测是重建能力前置条件的一部分。
- 被 `needs_agent` 唤回后，必须重新读取连接元数据，并使用 `query_logs` 查询失败 probe
  之后的新连接日志；需要明确记录连接池代次是否改变、DNS 缓存代次是否未变，以及日志中
  是否出现复用 resolver cache 的新证据。将这些新证据引用到第二周期 Plan。若刷新已成功、
  probe 已 `hard_stop` 且重建能力可用，则提交“重建连接池/清理 DNS 缓存 → probe”；高风险
  能力是否审批由 Harness 决定。
- 上述日志时间窗必须以 `harness_facts.virtual_time` 为基准，禁止使用 `generated_at`、
  `observed_at` 或现实当前日期。无法从虚拟时间可靠推导起点时省略 `from`/`to`，不要提交
  返回空结果的时间窗作为 resolver-cache 证据。
- 只有最近一次 probe 的 `output.outcome=healthy` 才能继续到显式 recovery Stage；probe
  本身不能直接把 Incident 标记为 `succeeded`。第二周期 probe 仍
  `hard_stop`、重建能力不可用、审批被拒或执行失败时，才认为没有剩余安全自治路径并升级人工。

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
