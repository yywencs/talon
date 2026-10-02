# 运行生命周期与持久化边界

当前支持保存、读取运行检查点，以及从中重建 Workflow 对象；不包含 Resume CLI、跨层对账或进程重启后自动续跑。`run_checkpoints` 是最近一个已提交保存边界，不能当作当前实时状态或完整进程镜像。

## 主调用链

```text
app.Run
  prepareRun                         输入默认值、场景与 Prompt、版本信息
  Recorder + 新建 Workflow          初始化审计，保存 created
  Simulator 初始化到事件时刻          此后开始 tracing
  buildInvestigator / assembleController
  advanceRun                        时钟生命周期 + Controller + 审批循环
    IncidentController.Run
      recordingInvestigator         调查审计：开始 → 调用 → 结束 → 写入
        intentPersistenceGate       调查返回 → 保存 intent_accepted（含错误/取消）
      ExecutionCoordinator          DryRun → Policy → 受控执行
        saveAwaitingApproval        审批单与 awaiting_approval 同事务
      Workflow.EvaluateCheckpoint   业务阶段判定 → 下一阶段/再调查/结束
  取消并等待模拟器时钟退出             合并时钟错误，捕获稳定世界状态，输出结果
  tracing 收尾 → finishRunAudit      无论正常返回还是失败，尽力保存最终审计
```

`prepare.go` 只持有准备好的输入；`components.go` 组装已有 Workflow 和平台上的组件，不创建运行、不启动时钟；`lifecycle.go` 持有运行推进和模拟器时钟的生存期；`audit.go` 负责审计和最终结果。没有持有所有依赖的大服务对象。数据库连接仍由调用方持有和关闭。

## 三种不同含义

| 概念 | 所有者 | 职责 |
| --- | --- | --- |
| `workflow.DecisionCheckpoint` | Workflow | 根据业务阶段结果选择继续、重新调查或终止；保留现有状态值 `checkpoint` |
| RunArtifact | `runartifact.Recorder` 与审计调用方 | 保存调查、工具、阶段与最终结果；`WithWorkflowAudit` 的错误不能被执行失败转调查逻辑吞掉 |
| RunCheckpoint | app 的 `runCheckpointWriter`、`checkpoint.Data` 与 storage | 保存指定边界的运行状态，独立管理 revision；不包含完整 Agent 对话或 Simulator 世界 |

## 保存边界的所有权

| 边界 | 触发者 | 数据构造与门禁 | 存储与事务 |
| --- | --- | --- | --- |
| `created` | `app.Run`，新建 Workflow 后 | writer 从不可变元信息、用量与 Workflow 快照构造；失败停止启动 | `checkpoint.Store.Save` |
| `intent_accepted` | `intentPersistenceGate`，每轮调查返回后 | writer 只保存 validating 中的新冻结意图；即使调查错误或取消仍尝试保存；返回前不能进入 DryRun | `checkpoint.Store.Save` |
| `awaiting_approval` | `ExecutionCoordinator.EvaluatePolicy` 构造审批请求 | app 接入 `WithApprovalCheckpoint`；writer 构造该时刻快照并同步提交；失败停止推进 | `SaveWithApprovals` 创建审批请求并 CAS 写检查点，整体提交或回滚 |

独立使用 ExecutionCoordinator 时，未接入运行检查点的调用方仍可只使用审批 Store；完整 app 始终组装原子保存回调。审批决定仍由审批 Store 与 Workflow 校验，不改变既有审批语义。

writer 不读取 Recorder.Snapshot。初始化时从 Recorder.Metadata 取得 `runmeta.Metadata` 值副本；模型调用用量通过 `ModelCallsUsed` 小接口读取，包含未结束的当前调查。共享元信息类型只有来源版本和运行配置，没有通用 common 包。JSON 字段与格式版本保持不变。

Workflow 保留状态转换和冻结动作规则；checkpoint 模型校验保存边界的数据不变量及审批一致性；storage 在所有读写入口执行校验，负责编码、SQL、JSONB、事务、条件更新。保存失败在 writer 内锁定，当前进程不能拿未提交状态继续推进。存储接口本身仍支持相同预期 revision、相同内容的幂等重试。

## 取消与错误收尾

调查提交意图后返回错误，原始错误与保存错误通过 `errors.Join` 一同传播。调查审计在门禁之后结束，因此包含保存失败。调用方取消不会直接取消这些尽力写入，每次检查点、调查审计和最终审计各有最长 5 秒的保存窗口。

`advanceRun` 取消并 join 模拟器时钟后才返回。正常的时钟取消不制造额外故障；真正的时钟错误与运行错误合并。最终审计读取的世界不会再被该时钟推进。普通执行失败允许按领域规则返回调查，审计持久化错误则必须停止。

## 从数据库重建 Workflow

```go
loaded, err := app.LoadWorkflow(ctx, database.Checkpoints(), runID)
if err != nil {
    return err
}
saved := loaded.Checkpoint // 原 run_id、revision、运行配置与模型调用计数
flow := loaded.Workflow   // 重建后的独立内存对象，尚未对账
_ = saved
_ = flow
```

`LoadWorkflow` 检查格式、运行身份和存储版本，再调用 `workflow.Restore`。Restore 接受从 protected 开始并包含完整历史的快照，校验状态转换、预算计数、当前意图与历史意图的一致性、阶段位置、动作及当前审批的绑定。它直接复制状态，不重放事件或平台动作，锁重新初始化，后续事件使用当前进程时钟。原有 digest 原样保留，不使用 JSONB 规范化后的数字文本重新计算。

恢复出来的对象与读取记录不共享可变字段；返回的检查点保留元信息和独立 revision，不自动建立检查点写入器，也不将模型调用计数注入新 Agent。数字继续以 `json.Number` 保留精度；规则比较沿用现有数值比较语义并支持该类型。

读取不存在的运行、未知格式、身份不匹配或结构不一致时返回错误，不退回创建新运行。恢复本身不写检查点或审批单。即使审批表中已有批准结果，等待审批快照也仍保持等待审批，留给后续显式对账。

## 后续续跑接入位置

未来续跑入口可以调用 `LoadWorkflow`，完成审批和执行记录对账后，再复用组件组装与推进路径；不能用 `NewIncidentWorkflow(Config{InitialState: ...})` 代替恢复。需要另外设计 Agent 用量/上下文恢复、平台状态重建或重连、执行租约与 Operation 对账，以及检查点落后于执行状态时的处理。

执行层已有租约、Operation 查询和 unknown 处理保持原样；此次不扩大保存边界，不引入终态快照、事件溯源或恢复框架。业务状态机、冻结参数、ID、digest 和幂等键语义未改变。终态与评测继续使用 RunArtifact。
