# 独立 Simulator（本地开发）

服务复用 `internal/simulator`。每个进程只加载一个场景，每次启动生成随机的 `environment_id`。Agent 退出不会删除世界、停止服务时钟或取消已经接受的操作。服务自身重启不会恢复世界，也不会恢复操作或 epoch；旧任务连接新实例会被拒绝。此接口没有用户认证，不应暴露到公网或不可信网络。

## 启动

在仓库根目录运行：

```sh
go run ./cmd/talon-simulator --dataset data/toolops-v1 \
  --scenario mapping-regression-rollback-001 --listen 127.0.0.1:8090 \
  --clock-interval 20ms

# 使用 .env 中现有 LLM 配置及 DATABASE_DSN（须先在专用数据库执行现有 SQL 迁移）
go run ./cmd/talon run --simulator-url http://127.0.0.1:8090 \
  --env-file .env --timeout 30m --approval-poll 1s

# 用启动输出的原 run_id 手动续跑
go run ./cmd/talon resume --run-id YOUR_RUN_ID \
  --simulator-url http://127.0.0.1:8090 --env-file .env \
  --timeout 30m --approval-poll 1s
```

HTTP 请求默认超时 5 秒，可通过 `--http-timeout` 配置。`--sqlite-test-db /tmp/talon-local.db` 可替换 PostgreSQL，仅用于本地开发/验收，自动初始化测试 schema。模型路径仍使用现有 LLM 配置。旧 `talon --scenario ...` 内嵌场景入口保留；外部路径始终调用 `RunConnected` / `Resume`，不创建本地替代世界。打印新运行的 run_id、environment_id，续跑打印 waiting、reason、state、final。审批仍由现有 Approval Store 的显式人工决定处理；外部路径不使用 `--auto-approve`。

`--approval-poll` 默认一秒，使人工审批在同一进程继续。设置零表示等待审批时退出。若退出再续跑，会重新调查并要求新方案重新审批，旧审批不能授权新方案。进程被强制杀死后，任务租约最长需等待 30 秒过期，动作租约也必须过期；不用手动删除租约。

## 时钟与退出

服务先推进到场景 `clock.incident_at`，未配置则推进到最早 timeline 事件。之后每个真实 `clock-interval`，在有 pending/running 操作时推进一个场景 tick；无活动操作时暂停。这沿用内嵌场景的规则，让调查和审批等待不耗尽场景。该规则不依赖 Agent 是否连接；服务独立推进已接受操作及期间的 timeline。推进在 end_at 截止，到期仍未结束的操作保留未知/运行状态，不虚构成功。健康与 info 仍可查询。SIGINT/SIGTERM 停止服务时钟并关闭 HTTP 服务；服务退出后内存世界丢失。

## 协议

- `GET /healthz`：HTTP 200（进程可服务，不代表所有业务操作可完成）。
- `POST /v1/info`，body `null`：返回环境、incident、`dataset_version`、虚拟时钟、tick、end_at 与时钟规则。`dataset_version` 来自服务加载后 `Dataset.Version`（dataset.yaml 的声明值），不是目录名。
- `POST /v1/connect`：body 是 `platform.RunConnection` 的 JSON（现有字段 `environment_id`、`incident_id`、`run_id`、`epoch`）。
- `POST /v1/<ToolOpsPlatform 方法名>`：覆盖全部查询、ExecuteRemediation（含 DryRun）、RequestProbe、RequestRecovery、EscalateIncident、GetOperation。
- `POST /v1/GetOperationByKey`：只读查询原幂等键，payload 为 `{"incident_id":"...","idempotency_key":"..."}`。

平台请求封装为 `{"binding":<RunConnection>,"payload":<现有 platform 请求 JSON>}`。响应是 `{"result":<平台返回值>,"error":{"code":"...","message":"..."}}`；成功省略 error，拒绝可以同时包含已存储的 rejected Operation。请求上限 1 MiB。JSON 解码使用 `UseNumber`，动作参数保留大整数。Simulator 的整数转换直接解析 `json.Number` 或原生整数，不经 float64；浮点值必须有限、无小数且在整数范围内，JSON 整数采用十进制整数字面量。`expected_pool_generation` 存在但类型非法、带小数或越界时返回 precondition_failed；有效但不匹配时返回 conflict。字段缺失是否允许仍由工具定义决定，非法字段不能绕过校验。Go 客户端实现 `ToolOpsPlatform`、`RunConnector` 及窄的 `OperationKeyReader`。

错误映射：not_found → 404；unauthorized → 403；conflict / stale_epoch / environment_mismatch → 409；precondition_failed / unsupported → 400；内部异常 → 500。客户端保留对应平台 sentinel 错误。网络中断、超时、无法解析的写响应及未知服务错误使用 `ErrOutcomeUnknown`，不能解释为明确未执行。context 传播给 HTTP 请求；服务接受阶段脱离请求取消，客户端断开不能撤销已接受业务。客户端不自动重试写请求，不设置 HTTP Idempotency-Key 重试头，也不跟随重定向。

## 外部数据集版本

`NewServer(sim, dataset.Version)` 必须传入加载后的非空版本，服务将它保留为不可变元信息。HTTP 客户端实现只读 `EnvironmentMetadataReader`。外部新运行从服务取得版本，保存到 RunArtifact 和 RunCheckpoint；Agent 本地 `--dataset` 不覆盖服务版本。显式配置与服务声明冲突时报错，缺失版本也报错。非 HTTP 连接器若不提供元信息接口，必须显式提供可信版本，不能借用本地目录名。

Resume 先用原检查点身份连接，再核对服务版本与原版本是否一致；缺失或冲突时停止，不改写历史。当前 CLI 的 Info 只用于查询当前服务，新运行填入版本，续跑仍以保存的身份和 Provenance 为准。内嵌场景入口保留原数据集加载行为。

## 数据流与原子 fencing

Agent 先获取数据库任务租约，再向原 environment_id 激活 run_id 的 epoch。检查点保存原身份；Resume 使用保存的身份连接，不能用 info 返回的新身份覆盖旧身份。相同 epoch 重连幂等，更高 epoch 接管，低 epoch 拒绝。返回的客户端持有自身绑定；所有平台方法（包括只读 Operation 查询）携带绑定，所有写入口（包括 DryRun 和升级）检查当前 epoch。没有无绑定的普通写入口。

服务 `Server.mu` 从检查绑定、比较幂等请求起，覆盖调用原 Simulator 的完整同步接受过程、登记操作与返回对象。connect 激活 epoch 也持有同一锁。原 Simulator 的 world 锁嵌套在此锁内；时钟只持有 world 锁，不反向获取服务锁。若旧写先进入锁，它先被接受，接管等它完成登记；若接管先进入锁，旧写即被拒绝。异步完成不持有服务锁；接管不会撤销旧操作，新 Agent 可查询它。

业务键作用域为 `(environment_id, run_id, 原幂等键)`，跨 epoch 不变。服务将 `(run_id, key)` 无歧义编码给现有 Simulator，外部响应还原原键；不会自动生成新业务键。服务保存完整请求的规范 JSON（含动作方法、参数、expected version 和 dry_run）指纹。相同键和相同请求返回原操作当前状态；不同请求返回冲突。Operation ID 查询也核对所属 run，不能用其他任务的 ID 找回操作。公共环境查询仍共享单场景观测；本接口不是多租户安全隔离。

## 响应未知后的找回

Resume 先接管，然后只读对账。已有 ID 时查 ID，缺少 ID 时通过只读 `OperationKeyReader` 查询原键。对账验证 incident、动作类型、名称和键；HTTP 服务额外限制所属环境与 run。找到 ID 后返回 `bind_operation`，由应用调用 Storage 的窄条件更新：事务内锁定/核验 run lease，并要求原 action 的 status、digest、intent、key、空 ID 匹配，且动作租约为空或已过期。不抢占活动动作租约。随后重新读取执行记录；非终态继续等待，终态使用既有 `CommitObservedExecution` 条件更新，之后保留结果和历史重新调查当前环境。

找不到可能是原请求仍在途，不能判定从未执行；返回等待，不生成新请求。远程平台的 Worker 轮询也使用只读找回，不因查询暂时缺失重提旧动作。本地操作等待期限届满也不代表独立服务已经终止操作：远程 Worker 继续只读轮询，调用方总超时可以结束当前等待，但不写成平台失败、不授权新修复。确认成功和确认失败都重新调查；旧计划不会直接继续探测/恢复，新方案仍需 DryRun、策略及当前审批。

## 自动化跨进程验收

```sh
go test ./internal/app -run '^TestCrossProcessAcceptedOperationResume$' -v -count=1
go test -race ./internal/simulatorhttp ./internal/app ./internal/controller ./internal/storage
go test ./...
```

跨进程测试通过同一个 Go 测试二进制启动三个独立 OS 进程：Simulator 服务、原 Agent、续跑 Agent。采用确定性 Investigator 与临时 SQLite。显式人工审批后，服务在接受修复但发送响应前发出同步信号；父进程确认数据库无 Operation ID，再 SIGKILL 原 Agent。信号释放后服务独立时钟推进原操作。父进程按键验证原 ID 已成功，再启动续跑 Agent，等待实际租约到期后通过 `app.Resume` 找回并写入 ID/终态，在原修复成功后，通过测试专用同步命令推进 timeline 注入新的 endpoint 故障，再重新读取当前 config 和 provider，验证原故障已修复且新故障必须来自当前观测，保留原历史。断言服务仅有 DryRun 和原修复两个操作，未重放旧修复/探测/恢复。此测试验证真实 HTTP 与数据库链路；它不调用真实 LLM，也不将模型随机输出当作验收条件。

PostgreSQL 实库测试仅在显式设置专用 `TALON_TEST_POSTGRES_DSN` 时运行；默认跳过，不读取 DATABASE_DSN 来做测试迁移。没有服务世界持久化、后台扫描、自动接管、多租户、跨 run 资源调度或公网认证。
