# 运行生命周期与持久化边界

当前支持保存、重建、只读对账，以及面向独立外部环境的手动续跑 API；包含本地独立 Simulator HTTP 适配器和 run/resume CLI；不包含后台自动接管调度。`run_checkpoints` 是最近一个已提交保存边界，不能当作当前实时状态或完整进程镜像。

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

## 只读对账 API

`app.LoadAndReconcileWorkflow(ctx, database, runID, operationReader)` 在 `LoadWorkflow` 后调用 `controller.ReconcileWorkflow`。对账使用只读的审批、执行和 Operation 接口，在独立 Workflow 副本上补入事实，错误时不返回半完成对象。审批按当前 Action ID 精确查询，核对 Incident、Intent、Action、digest、工具、DryRun Operation 和风险；保留原审批人、决定、原因和决定时间，不调用审批写接口。

| Next | 含义 |
| --- | --- |
| `investigate` | 尚未开始调查，或审批拒绝后返回调查 |
| `validate` | 校验阶段未发现执行记录，仍需正常预执行和策略判断 |
| `await_approval` | 部分或全部审批仍待处理 |
| `execute` | 当前授权已核对，尚有未开始动作；后续 Worker 仍需正常获取租约 |
| `evaluate_stage` | 当前阶段的持久化成功记录及 Operation 输出已补回，可做业务判定 |
| `inspect_failure` | 已将确定失败记录交给 Workflow 的失败规则，需检查其状态出口 |
| `query_operation` | 执行仍非终态、结果未知、查询失败或缺少原 Operation 信息，不得直接重做 |
| `commit_execution` | 查到终态，但执行记录尚未终结，必须由租约持有者先写入 |
| `recover_more_history` | validating 检查点后已有执行，或记录涉及其他阶段，当前快照不足以安全推进 |

执行记录不保存完整输出，因此补回成功动作时查询原 Operation，并核对原 Operation ID、Incident、类型、名称及幂等键。数据冲突返回 `ErrReconcileConflict`；超时、取消和未知失败不能直接当成“动作未发生”。对账不调用 Prepare、Claim、Complete 或平台写接口，也不自动做下一阶段判定。

这是一组观察结果，不是跨数据库与平台的原子快照，更不授予执行权限。这个只读 API 保持当前阶段的语义；实际续跑使用下面的保守流程，不根据 `execute` 直接继续旧计划。

## 独立环境的续跑流程

```text
app.Resume(run_id)
  原子获取 run_leases：owner_id + epoch + 到期时间
  LoadWorkflow，终态任务直接返回
  Connector.ConnectRun：确认 EnvironmentID，服务端激活 epoch
  ReconcileForResume：读取所有保留的 Intent 的执行记录
    尚未提交                 不执行旧计划
    running / unknown        查询原 Operation，不重新提交
    平台已明确结束、执行表滞后  任务租约 + 记录条件更新提交结果
                              活动 Action 租约未过期时不抢占
    已确认成功或失败          补回结果；不执行旧阶段判定规则
    缺少 Operation ID         只读按原键找回；先按租约条件写入 ID
    查不到 Operation / 缺历史 返回 Waiting，不猜测
  ResumeInvestigation：保留原计划、结果、预算，转 investigating
  保存 progress，保留原 run_id 并从原 revision 继续 CAS
  恢复审计和用量，构建新 Agent，要求读取当前证据
  原 Controller：新方案 → DryRun → 策略/审批 → 修复—探测—恢复
  保存进度，停止续租并释放当前任务所有权
```

重新调查会携带旧诊断、旧方案和确认结果，并要求区分“原故障仍在”“原故障已解决”和“发生新故障”。旧审批不会授权新 Intent。对账会查询失败动作的原 Operation；`cancelled`、查询不到、缺少原 Operation ID 或不一致的记录均不作为可安全继续的证据。查询异常返回等待，身份/结果冲突返回错误。

完整成功的当前 Stage 仍计入原阶段预算；恢复调查计入 `MaxAgentResumes`，耗尽则升级人工，不重置预算。已经 resolved/failed/blocked/escalated 的任务不自动重开。重复中断可以继续读取最新 progress；历史操作结果留在 Workflow，调查审计追加到原 RunArtifact。

`progress` 是外部环境入口新增的保存边界：包括调查开始、执行授权（提交动作之前）、阶段推进、终态和错误收尾。等待审批仍通过原有原子事务保存，不用普通 progress 绕过审批约束。旧三边界检查点如果已经落后到缺少冻结参数/授权历史，续跑返回等待补充历史，不能用当前环境重新计算旧参数再假装完成对账。

### 接入方式

独立 Simulator 已提供 `simulatorhttp.Client` 实现 `platform.RunConnector`，详见 [HTTP 服务与跨进程验收](simulator-http.md)：

- `EnvironmentID` 是环境实例的稳定标识，不能只用可重复的场景名；服务端必须核对实例身份。
- `ConnectRun` 在平台端原子激活对应 run 的 epoch，返回绑定该 epoch 的客户端。所有写接口在服务端检查 epoch；更旧 epoch 被拒绝。已接受的异步操作继续运行、仍可查询。
- 服务端在当前环境实例内保存 epoch 和操作记录。第一版不恢复服务世界；每次服务重启更换 EnvironmentID 并拒绝旧身份。只在客户端查一次数据库租约不足以实现该接口契约，不提供静默退回普通客户端的路径。

```go
connected := app.ConnectedConfig{
    Config: app.Config{
        Storage: database,
        ScenarioID: incidentID,
        Model: chatModel,
        Provenance: deploymentProvenance,
        RunConfig: runmeta.Config{EnvironmentID: environmentInstanceID},
    },
    Connector: simulatorConnector, // 部署侧实现 platform.RunConnector
    ApprovalPollInterval: time.Second,
}
// 新建时使用 RunConnected；中断后使用保存的 run_id。
result, err := app.Resume(ctx, app.ResumeConfig{
    ConnectedConfig: connected,
    RunID: runID,
})
if err != nil { return err }
if result.Waiting {
    // 展示 result.Reason，由调用方稍后重试 Resume 或交人工处理。
}
```

设置正的 `ApprovalPollInterval`，同一进程会等待外部审批决定，随后推进当前计划；不会替人审批，也不会重新调查。设为零则在等待审批处返回。若选择退出再 Resume，将按恢复规则重新调查并产生新的审批需求；需要持续处理审批的部署应启用轮询，避免“每次审批后重启、每次重启又重新审批”。Simulator 的虚拟时钟始终由独立服务维护，应用入口不创建或重置它。外部新运行的数据集版本来自服务加载后的 Dataset.Version，保存到检查点及审计；本地 --dataset 不提供外部版本。Resume 保留原版本和身份，并核对服务元信息，缺失或冲突时拒绝续跑。

模型用量从原审计和检查点恢复，以两者可确认的最大值为下限；新模型包装器从该值继续计数。进程在模型请求期间被杀死仍可能丢失未记账调用，这是现有尽力记账的边界，不是严格的跨进程调用额度预留。Prompt digest 存在时必须匹配当前加载内容。

### 并发与持久化边界

`run_leases` 使用数据库时间，默认租期 30 秒，每 10 秒续租；续租失败取消当前运行。每次接管（包括主动释放后的接管）递增 epoch。检查点、执行记录与审计的写入通过 `Storage.OwnedRun` 在事务中锁定并验证租约行，失去所有权的进程不能覆盖新进度。平台写入还必须由上述服务端 fencing 阻止。所有参与同一任务的执行进程都必须使用这条受控路径。

任务租约按 run_id 隔离，不是不同任务对同一资源的互斥锁。资源级冲突控制仍由平台承担。动作终态写回不会抢占仍有效的动作租约，也不会为了找回结果提交旧动作。

需要执行数据库迁移 `007_run_leases`；代码不会替生产环境执行迁移。没有环境实例身份的旧检查点拒绝续跑，避免连接到重建的初始场景。最终世界快照仍属于外部服务，此入口不生成 Simulator 全量世界快照。
