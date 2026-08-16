# Baseline v1 问题记录

本文记录 `v1.0.0`、`toolops-v1` 第一版 Baseline 中暴露的问题。三个场景各运行
3 次，共 9 次；成功 3 次，4 次运行因 `exceeds max steps` 失败。

## 1. 终态动作成功后 Agent 没有停止

- **状态**：已修复。
- **出现情况**：一次 mapping 运行已经成功调用 `submit_plan`，Workflow 进入
  `planned`，之后仍因超过最大步数被记为失败。
- **原因**：ReAct Harness 在终态工具成功后仍要求模型继续生成下一轮回复，没有把
  `submit_plan` 等终态动作作为循环结束条件。
- **解决**：Tool Guard 在 `submit_plan` 或 `escalate_incident` 成功且 Workflow 确实进入
  `planned` 或 `escalated` 后，通过 Eino 的直接返回信号结束 ReAct。工具执行失败时不
  结束，模型仍可在下一轮修正参数。

## 2. 调查阶段调用了过多工具

- **出现情况**：connection 和 credential 运行中，Agent 查询了大量无关或重复的指标、
  日志、链路、配置和连接信息，在提交方案或升级事件前耗尽步数。
- **原因**：Agent 缺少按故障类型选择工具的约束，也缺少“证据已经足够”的停止判断，
  因此倾向于遍历可用的观测工具。

## 3. Connection 版本参数被错误理解

- **出现情况**：Agent 观察到 `pool_generation=12` 后提交了
  `expected_pool_generation=13`，触发状态冲突，并多次重新调查和提交计划。
- **原因**：模型把 `expected_pool_generation` 理解成操作后的目标版本，而它实际表示
  执行动作前观察到的当前版本；现有工具参数说明没有清楚表达这个并发控制语义。

## 4. Connection 在最终状态未恢复时仍被判定完成

- **出现情况**：一次运行被 Workflow 标记为 `resolved`，但 FinalState 中仍保留旧 IP
  `192.0.2.10`，没有恢复到预期的 `192.0.2.25`。
- **原因**：Workflow 的完成判断侧重恢复动作或探测是否成功，没有同时验证解析 IP、
  缓存和活动连接等最终状态是否满足场景要求。

## 5. Credential 升级原因缺少结构化字段

- **出现情况**：Agent 正确调用了 `escalate_incident`，也提供了文字原因和证据，但
  Evaluator 无法确认它是否满足 `no_safe_remediation_available` 这一期望。
- **原因**：升级动作目前只保存自由文本 `reason`，没有稳定的 `reason_code`，因此无法
  进行确定性评测。

## 6. 预期升级在 CLI 中仍表现为错误

- **出现情况**：credential 场景按设计升级人工后，Artifact 为
  `completed/escalated`，但 CLI 仍以非零状态退出并输出错误。
- **原因**：CLI 目前没有区分业务上的预期升级和程序执行失败，两者共用了错误退出语义。

## 7. 部分期望暂时无法评测

- **出现情况**：当前平均 coverage 为 76.39%，受保护路由的介入前快照、事故检测时间、
  自由文本根因、Evidence 语义映射和结构化 Experience 等检查会被跳过。
- **原因**：Run Artifact 尚未保存对应的结构化事实，或现有数据缺少与 expectation 的
  稳定关联，Evaluator 无法据此作出确定性判断。
