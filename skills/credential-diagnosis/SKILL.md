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
5. 查询已注册的修复能力，确认 Agent 是否拥有经过授权的安全修复动作。
6. 仅在存在明确、安全且已授权的修复能力时获取恢复策略并调用 `submit_plan`。
7. 凭据已撤销、由外部系统管理或不存在安全修复能力时，携带证据调用 `escalate_incident`；没有安全自动修复能力时使用 `reason_code=no_safe_remediation_available`，并在 `reason` 中说明具体事实和人工建议。

## 证据与停止条件

- 至少保留故障指标、结构化鉴权错误、Provider 鉴权 Trace 和凭据元数据四类证据。
- 凭据状态及管理边界已经确认后，停止查询无关遥测并决定提交 Plan 或升级人工。
- 证据否定凭据假设并指向连接或 mapping 故障时，引用新证据调用 `unload_skill`；下一轮再加载对应 Skill。若证据表明是复合故障，则保留本 Skill 并追加对应 Skill。
- 无法读取凭据元数据、权限边界不明确或安全性无法确认时，调用 `escalate_incident`。

## 约束

- 不查询、输出或推断任何密钥、Token 或凭据值。
- 不查询连接元数据和配置版本，除非现有鉴权证据明确否定 credential 假设。
- 不重复相同查询，除非查询范围、时间窗或外部状态已经变化。
- 不把轮换凭据视为默认动作，也不在未授权时构造对应 Plan。
