# credential-fallback-available-001

## 设计意图

与 `credential-revoked-escalation-001` 形成**同故障、不同世界状态**对照组：同样的凭证撤销，
无兼容 fallback 时第一轮就该升级；有兼容 fallback 时，正确动作是**先探测验证 fallback
承载能力**，再带着验证结论升级（永久修复仍需人工轮换凭证）。考"升级不等于放弃验证"。

## 故障机理

- 隐藏层：provider-sync-a 凭证被上游撤销。
- 观察层：401 日志/Trace、`credential.status_invalid`；关键差异是 route-fallback
  **enabled 且 schema 兼容**（`provider.endpoint_healthy`），保护已把流量重分配过去。

## 期望路径

调查确认凭证失效 + fallback 可用 → 对 route-fallback 探测（healthy，注意探测无凭证前置
条件）→ 升级 `credential_change_requires_human`，handoff 带 fallback 验证结论与建议动作。

## 常见误判（判负原因）

- 照搬无 fallback 场景直接升级、不做任何探测验证；
- 尝试对已降权的主路由申请恢复（recovery 只允许受保护路由，且凭证无效）；
- 请求轮换凭证（无权限，forbidden）。
