---
name: credential-diagnosis
description: >-
  调查凭据撤销、过期、权限不足和 Provider 鉴权失败。Use when 日志或指标显示 401、403、unauthorized、permission denied、credential revoked 或相近鉴权证据。Don't use when 主要证据是 DNS、socket、连接池错误、字段类型错误、Schema 校验失败或配置映射回归；这些情况应使用 connection-diagnosis 或 mapping-diagnosis。
---

# Credential Diagnosis

确认鉴权失败是否源于凭据状态，并在无法安全自治修复时尽快升级人工。

## 调查流程

1. 使用公共范围和指标工具确认受影响的服务、路由、Provider、错误率和故障时间窗。
2. 调用 `query_logs` 确认结构化鉴权错误码和受影响 Provider；不要仅凭 HTTP 状态码判断凭据已撤销。
3. 调用公共只读工具 `query_traces`，确认请求是否到达 Provider，以及 Provider 是否返回 401、403 或等价鉴权状态；引用能够直接证明该交互的 Trace 证据。
4. 调用 `get_credential_metadata` 读取凭据 ID、状态和管理方；不得请求或推断密钥内容。
5. 查询路由、Provider 和已注册修复能力，明确主路由当前凭据状态、fallback 是否启用且 schema 兼容，以及 Agent 的权限边界。
6. 按当前状态选择唯一适用路径：
   - 凭据为 `active` 或证据表明平台轮换已经生效：先对主路由提交探测 Stage；健康时 `continue` 到显式恢复 Stage，恢复成功后才关闭事件。
   - 主凭据无效，但存在已启用且 schema 兼容的 fallback：先对 fallback 提交验证型探测 Stage；健康规则选择 `needs_agent`，不要附带恢复 Stage。Harness 唤回后保持当前保护，用 `credential_change_requires_human` 升级并移交 fallback 验证结论与人工切换建议。
   - 主凭据无效，且没有启用、兼容的 fallback：探测不适用，不要为满足流程而强行探测；直接使用 `no_safe_remediation_available` 升级，并引用证明 fallback 不可用的路由和 Provider 证据。
7. 只有存在明确、安全且已授权的自治修复动作时，才把该修复动作纳入 Execution Intent；由外部系统管理本身不等于可以跳过上面的当前状态与 fallback 判别。

## 证据与停止条件

- 至少保留故障指标、结构化鉴权错误、Provider 鉴权 Trace 和凭据元数据四类证据。
- 升级前必须查询路由与备选 Provider 状态（get_routes/get_providers）。断言没有兼容回退时必须引用否定证据；存在兼容 fallback 时必须先探测验证，不能仅凭静态 Provider 健康状态升级。
- 凭据状态及管理边界已经确认后，停止查询无关遥测并决定提交 Execution Intent 或升级人工。
- 证据否定凭据假设并指向连接或 mapping 故障时，引用新证据调用 `unload_skill`；下一轮再加载对应 Skill。若证据表明是复合故障，则保留本 Skill 并追加对应 Skill。
- 无法读取凭据元数据、权限边界不明确或安全性无法确认时，调用 `escalate_incident`。

## 约束

- 不查询、输出或推断任何密钥、Token 或凭据值。
- 不查询连接元数据和配置版本，除非现有鉴权证据明确否定 credential 假设。
- 不重复相同查询，除非查询范围、时间窗或外部状态已经变化。
- 不把轮换凭据视为默认动作，也不在未授权时构造对应 Execution Intent。
