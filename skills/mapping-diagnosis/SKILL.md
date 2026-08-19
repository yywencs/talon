---
name: mapping-diagnosis
description: >-
  调查工具参数映射、Schema 兼容性或配置版本回归。Use when 请求出现字段缺失、参数类型错误、序列化或校验失败，并且故障可能与配置变更相关。Don't use when 主要证据是 401/403、凭据撤销、DNS、socket、连接池或网络错误；这些情况应使用 credential-diagnosis 或 connection-diagnosis。
---

# Mapping Diagnosis

定位参数映射、Schema 或配置版本回归，并在证据充分时提交可验证的修复计划。

## 调查流程

1. 使用公共范围和指标工具确认受影响的服务、路由、Provider、错误率和故障时间窗。
2. 调用 `query_logs` 获取结构化错误码、失败字段和参数类型；不要仅凭一条日志确定根因。
3. 仅在需要确认失败链路或责任边界时调用 `query_traces`。
4. 调用 `get_change_records` 检查故障时间窗附近的配置或映射变更。
5. 调用 `get_config_versions` 对比当前版本和已知健康版本。
6. 在故障签名、时间关联和版本差异相互印证后，获取修复能力与恢复策略并调用 `submit_execution_intent`。

## 证据与停止条件

- 至少保留故障指标、结构化错误以及变更或版本差异三类证据。
- 已确认当前版本引入不兼容且存在已知健康版本时，停止继续扩展调查并提交 Execution Intent。
- 证据否定 mapping 假设并指向凭据或连接故障时，引用新证据调用 `unload_skill`；下一轮再加载对应 Skill。若证据表明是复合故障，则保留本 Skill 并追加对应 Skill。
- 关键遥测缺失、没有安全修复能力或无法确定健康目标版本时，调用 `escalate_incident`。

## 约束

- 不查询凭据或连接元数据，除非已有证据明确否定 mapping 假设。
- 不重复相同查询，除非查询范围、时间窗或外部状态已经变化。
- 不根据场景名称或单一错误文本猜测根因。
- 不直接执行修复；只通过 `submit_execution_intent` 提交结构化动作。
