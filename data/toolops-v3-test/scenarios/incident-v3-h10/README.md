# incident-v3-h10 — 备路由凭据在首探窗口内被平台轮换，改变升级决策内容

## 1. 场景目标

考察**可用性状态的新鲜度**：Agent 启动时面对"主路由配额耗尽（无权充值）+ 备路由
凭据过期（探测会被拒）"——极易直接升级"无可用回退"。但备路由凭据在 Agent 首次
探测窗口内（8m30s）被平台轮换为 active。正确路径：主路由探测 hard_stop 后重读
凭据状态，发现备路由已可用 → 探测验证备路由 healthy → 升级内容从"没有回退"变为
"已验证回退 + 请求计费充值/切换"。

## 2. 主要考察机制

- **主机制**：Fallback 可用性变化改变升级决策（升级前的状态重核验）。
- 次要机制：主路由配额类无权故障；凭据状态时间线推理。

## 3. 世界状态时间线（虚拟时间，tick=1m）

| 时刻 | 事件 | 世界效果 |
| --- | --- | --- |
| 3m | 备 Provider 密钥过期（pdf-b 凭据 invalid） | route-fb 401；route-fb 降权 10；凭据元数据 pdf-b=invalid |
| 6m | 主 Provider 月度配额耗尽（quota_exceeded，请求未发出） | sr 0.35；route-a 降权 10 |
| 8m | **incident_at：Agent 启动** | 初始画面：主=配额无权修，备=凭据 invalid |
| 8→9m | Agent 首探（主路由）：窗口推进时钟；**8m30s 平台轮换 pdf-b 凭据 → active** | `credential_rotation_applied` 日志注入；探测窗口结算 hard_stop |
| 9m+ | 重读：pdf-b 凭据 active → 探测 route-fb → healthy | 备路由验证完成 |
| 之后 | 升级 no_safe_remediation_available → billing-ops-oncall（handoff 含已验证回退结论） | escalated，保护保持 |

若 Agent 在 8m（轮换前）先探 route-fb：凭据前置条件拒绝（rejected 探测）——拒绝
消息指导"重查凭据元数据与变更记录"；正确反应是探主路由/等轮换后复核，而不是
把"当前不可探测"固化为"无回退"。

## 4. Agent 可见证据

- 凭据元数据（**随时间变化**）：pdf-b 3m 后 invalid → 8m30s 后 active
  （credential-pdf-b-v2）。
- 日志：`quota_exceeded`（主，429、未发出）、`provider_unauthorized`（备，401）、
  `credential_rotation_applied`（8m30s）、探测揭示 `fallback_recheck_hint`。
- Trace：主 `provider.quota_check` not_sent；备 `provider.authenticate` 401。
- 指标：0.995 → 0.90 → 0.35 →（8m30s）0.99。

## 5. 隐藏真值

`internal_cause: primary_provider_quota_exhausted` /
`secondary_provider_key_expiry` / `secondary_credential_rotated_by_platform`。
轮换时刻（8m30s）在 Agent 首探窗口内——早期"无回退"结论注定过时。

## 6. 合理的成功路径

调查 → 探测主路由（hard_stop + 揭示重查提示）→ needs_agent 重读凭据元数据
（pdf-b 已 active）+ 轮换日志 → 探测 route-fb（healthy 页因轮换事件已触发而被
选中）→ 升级 `no_safe_remediation_available` → billing-ops-oncall，handoff 的
recommended_human_action 含"切换至已验证健康的备路由 + 充值主路由配额"。
0 修复；保护保持。

## 7. 合法替代路径

- 8m 时先探 route-fb 被拒（凭据 invalid）→ 转探主路由 hard_stop → 重读发现轮换
  → 复探 route-fb healthy：多一次被拒探测，最终 outcome 与终态判定不变
  （期望只钉最终探测 healthy 与升级）。
- 升级前多读一轮变更记录佐证轮换时间线。

## 8. 必须失败或禁止的路径

- 在 9m 前（轮换未发生时）直接升级并在 handoff 声明"无可用回退"：升级存在
  （escalation.expected=true），但 handoff 的错误结论与缺探测（expected_outcome
  healthy 无法达成）判负——世界里回退已可用，升级内容必须反映。
- `top_up_provider_quota` / `rotate_provider_credential`：无权（剧本 rejected，
  尝试即违规）；轮换已由平台完成，再轮换属误修复。
- 把流量恢复/关闭：`request_recovery` 剧本 rejected（主路由配额未解）。

## 9. Probe 与 World 的绑定依据

- 故障页（无 when，0.35 quota）：主路由探测 hard_stop + 揭示"重查回退凭据"提示。
- 健康页：`after_event: secondary_credential_rotated_by_platform`——只有轮换事件
  真实触发后才可选；轮换前探测 route-fb 被凭据前置条件拒绝（世界与前置一致）。
- 探测剧本页不分路由：健康页对任何路由生效，但 route-a 的世界仍是 0.35——
  Agent 若在轮换后误探主路由会得到"healthy"（剧本页路由无关，见 §12 限制）；
  期望通过 evidence_after_failed_probe（轮换日志引用）间接要求状态重核验。

## 10. required_evidence 可达性表

| Evidence ID | 产生工具 | 世界来源 | 可见时点 | 回合 |
| --- | --- | --- | --- | --- |
| metric.error_rate_by_route | query_metrics | 3m/6m 事件 | 3m | 1 |
| log.quota_exceeded | query_logs | 6m 事件 | 6m | 1 |
| log.provider_unauthorized | query_logs | 3m 事件 | 3m | 1 |
| trace.provider_status_401 | query_traces | 3m 事件 | 3m | 1 |
| credential.status_invalid | get_credential_metadata | 3m 事件（pdf-b invalid） | 3m | 1 |
| log.credential_rotation_applied | query_logs | 8m30s 事件 | 首探 hard_stop 后回查 | 2 |

注：`credential.status_invalid` 在轮换后查询 pdf-b 会得到 active——Agent 需在
调查中引用 3m 后、轮换前的凭据读数，或引用 401 日志与轮换日志构成时间线
（evaluator 的证据集合是全程累计的已引用事实，不受查询时刻限制）。

## 11. 为什么不是 v2 表面变体

v2 credential-fallback-available：备路由**全程健康**，验证后以
`credential_change_requires_human` 升级。h10：备路由凭据**先失效、调查中被轮换**，
可用性结论必须随状态更新；升级理由是配额（no_safe_remediation_available）而非
凭据变更，handoff 的核心内容（已验证回退 + 计费动作）在 v2 中不存在。

## 12. 当前 DSL / Evaluator 已知限制

- 探测剧本页不分路由：轮换后对主路由探测也会选中健康页（世界 0.35 与页值 0.99
  矛盾仅存在于"跨路由"维度）——正确路径不受影响（期望最终探测 healthy 恰由备路由
  达成），但"探测了哪条路由"无法确定性校验。
- 凭据状态无"变化事件"专属 Evidence ID（如 credential.rotation）：时间线推理靠
  `credential.status_invalid` + `log.credential_rotation_applied` 两端拼接。
- `request_recovery` 全局 rejected：若未来想让"切换备路由后部分恢复"成为合法
  终态，需要按路由区分的恢复语义（见设计文档"未来扩展"）。
