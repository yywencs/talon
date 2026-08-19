# auth-negative-cache-window-001

## 设计意图

考察**历史证据与当前状态的时间窗判别**（对应真实平台鉴权负缓存 30s 时间窗机理）：
401 在检测窗口内真实发生过，但凭证已被平台安全在 8m 轮换恢复。正确行为是查**当前**
凭证状态与恢复日志，不重复轮换、不升级，探测后恢复流量。

## 故障机理

- 隐藏层：4m 凭证撤销 → 8m 平台轮换新凭证（credential-invoice-a-v6 active）。
- 观察层：历史 401 日志/Trace **仍然存在**（日志是历史事实，不会消失）；
  `log.credential_rotation_applied` 是恢复证据；当前凭证 ID 已变且 active。
- 探测带 `credential_status_must_be: active` 前置条件——当前态满足才会被接受。

## 期望路径

调查对账（历史 401 vs 当前 active + 轮换日志）→ Intent（探测 + 恢复）→ healthy →
恢复 → resolved。

## 常见误判（判负原因）

- 只看历史 401 就升级 `credential_change_requires_human`（凭证已经换好了）；
- 请求轮换凭证（forbidden，且会重复已完成的操作）；
- 探测带旧凭证预期（前置条件按当前状态校验）。
