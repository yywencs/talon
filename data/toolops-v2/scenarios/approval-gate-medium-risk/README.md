# approval-gate-medium-risk-001

## 设计意图

机制覆盖型场景：medium 风险回滚必经**审批门禁**，覆盖 `awaiting_approval` 状态与
审批收件箱路径（评测流水线 auto-approve=true 会自动批准，Artifact 中仍能看到
`action_policies` + `action_approvals` 的完整记录）。诊断与修复本身是标准 mapping 回归，
作为审批路径覆盖的最低成本载体。

## 故障机理

与 `mapping-regression-rollback-001` 同构（不同服务），差异点仅在风险策略：
`rollback_mapping` 声明 `risk: medium` + `requires_approval: true`。

## 期望路径

调查 → Intent（回滚 mapping-v1 + 探测）→ Dry Run 通过 → 策略判定 approval_required →
进入 awaiting_approval → 审批通过 → 执行 → 探测 healthy → 恢复 → resolved。

## 常见误判（判负原因）

- 跳过审批语义直接把提交当作已执行（accepted ≠ authorized ≠ succeeded）；
- 其他判负点与标准 mapping 场景一致。
