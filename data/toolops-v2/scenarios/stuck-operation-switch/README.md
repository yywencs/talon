# stuck-operation-switch-001

## 设计意图

考察**卡死操作的结构化处理**（对应真实平台已知缺陷"任务已受理但永不推进"）：
首选修复 `rebuild_provider_queue` completion_delay 30m，远超执行器 10 分钟操作超时——
操作停留在 pending/running， Harness 会把它收敛为 `timed_out` 结构化失败并唤回 Agent。
正确行为是接受超时事实，切换到备选方案 `force_queue_drain`，而不是换幂等键重试卡死操作。

## 故障机理

- 隐藏层：上游突发流量导致 Provider 请求队列死锁，队列内请求超时。
- 观察层：`log.queue_backlog_exceeded` + Trace `terminal_span: provider.queued`。
- 卡死语义：操作 accepted → 长期 pending → 执行器 `finishTimedOutAction` 以
  `action_operation_timed_out` 收敛 → needs_agent 带结构化失败回调查。

## 期望路径

Intent v1（rebuild_provider_queue）→ 操作超时收敛 → needs_agent → Intent v2
（force_queue_drain + 探测）→ healthy → 恢复 → resolved。

## 常见误判（判负原因）

- 对卡死操作换幂等键重复提交（等价于无限等待）；
- 超时后直接升级（仍有安全的备选能力，未到升级条件）；
- 忽视 `query_operation`/`get_tasks` 的执行状态观察。
