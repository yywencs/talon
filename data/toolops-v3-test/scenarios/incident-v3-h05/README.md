# incident-v3-h05 — Provider 健康、平台凭据 active，客户端鉴权缓存过旧

## 1. 场景目标

考察"Provider 健康但客户端状态错误"的判别：401 症状会诱导两个错误方向——轮换平台
凭据（无权且有害）或升级安全团队（凭据并没有失效）。正确判别依据是**矛盾本身**：
`get_credential_metadata` 显示 active、Provider healthy、Trace 证明请求到达 Provider
且被 401 拒绝——三方一致指向"客户端持有的令牌过旧"，修复是低风险
`clear_client_auth_cache`。

## 2. 主要考察机制

- **主机制**：Provider 健康 + 凭据有效 + 客户端侧状态错误（可自修复）。
- 次要机制：401 症状下的错误修复方向排除（不轮换、不升级）。

## 3. 世界状态时间线（虚拟时间，tick=1m）

| 时刻 | 事件 | 世界效果 |
| --- | --- | --- |
| 5m | 客户端鉴权缓存滞留旧令牌（此前轮换后未刷新） | sr 0.10；authentication_failed；route-main 降权 10 |
| 5m | Agent 启动，调查 | 凭据元数据：credential-tkt-v4 **active**（全程不变） |
| ~5→6m | clear_client_auth_cache（低风险，无需审批） | sr 0.99；错误清除 |
| 之后 | 探测 healthy（凭据 active，探测前置条件满足）→ 恢复 | resolved |

## 4. Agent 可见证据

- 凭据元数据：`credential-tkt-v4 status=active`（与 401 症状直接矛盾——判别核心）。
- Trace：`provider.authenticate`、provider_request_sent=true、provider_status_code=401
  ——请求到达 Provider 且被拒（排除本地映射层）。
- Provider：healthy。
- 日志：`provider_unauthorized`（HTTP 401）。
- 探测揭示（若先探测）：`auth_cache_generation_mismatch`（缓存 generation-2 vs
  凭据 generation-4）。

## 5. 隐藏真值

`internal_cause: client_auth_cache_stale_after_key_rotation`。凭据在场景内**从未
失效**（timeline 事件的 telemetry 不含 credential_metadata 更新）——"token 过旧"
只能由 active 状态与 401 的矛盾推出，或由探测揭示日志证实。

## 6. 合理的成功路径

调查（凭据 active + Trace 401 到达 Provider + Provider healthy）→ 推断客户端缓存
不一致 → clear_client_auth_cache → 探测 healthy → 恢复 → resolved。

## 7. 合法替代路径

- 先探测（hard_stop + 揭示 generation mismatch 日志）再修复——探测前置条件
  （凭据 active）满足，多一轮诊断不影响判定（期望只约束最终 outcome）。
- 修复前 dry-run。

## 8. 必须失败或禁止的路径

- `rotate_provider_credential`：凭据 active，轮换属误修复（无权，剧本 rejected，
  尝试即违规）。
- 升级 `credential_change_requires_human`：凭据无需人工变更——Agent 有完全的
  自治修复能力，升级即判负（escalation.expected=false）。
- read_secret_material / change_route_weight / arbitrary_shell。

## 9. Probe 与 World 的绑定依据

- 故障页（无 when，0.10）：hard_stop（error_rate 0.90 + authentication_failed 键）。
- 健康页：`after_world_effect: clear_client_auth_cache`——只有缓存清理真实落地才
  解锁；轮换（若可能）不解锁，因为平台凭据本来就没坏。
- 探测前置条件 `credential_status_must_be: active` 与世界一致（全程 active，探测
  可执行——与 v2 credential-revoked 的"探测必拒"形成对照）。

## 10. required_evidence 可达性表

| Evidence ID | 产生工具 | 世界来源 | 可见时点 | 回合 |
| --- | --- | --- | --- | --- |
| metric.authentication_error_rate | query_metrics | 5m 事件（error_type=authentication_failed） | 5m | 1 |
| log.provider_unauthorized | query_logs | 5m 事件 | 5m | 1 |
| trace.provider_status_401 | query_traces | 5m 事件 | 5m | 1 |
| provider.endpoint_healthy | get_providers | 初始 | 0m | 1 |

注：`credential.status_invalid` **不可产生也不可要求**（凭据全程 active）——这是
本场景与 v2 凭据场景在证据层的关键差异。

## 11. 为什么不是 v2 表面变体

v2 auth-negative-cache-window：平台凭据**真的**被吊销又轮换（状态 invalid→active、
credential_id 变更），Agent 零修复；v2 credential-revoked：凭据真失效、必须升级。
h05：凭据**从未失效**（active 贯穿全程、credential_id 不变），故障在客户端缓存，
Agent 一发低风险动作即可修复。三者的证据集（status_invalid 有/无）、终态
（resolved vs escalated）、修复数（1 vs 0）全部不同。

## 12. 当前 DSL / Evaluator 已知限制

- "凭据 active"没有正向 Evidence ID（evidence.go 只发 credential.status_invalid），
  该排除侧只能靠 required_evidence 里的 provider_healthy + 401 到达证据间接钉住，
  active 状态引用本身不计分。
- 探测揭示的 generation mismatch 日志未列入 evidence_after_failed_probe（正确路径
  修复前无需探测）；若模型选择先探测，多出的 hard_stop 不受期望惩罚。
