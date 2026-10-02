# Talon 数据库脚本

这里保存 Talon 控制面数据库的建表和回滚脚本。目前包含 Action 审批、Action Execution、结构化 RunArtifact 和运行检查点。RunArtifact 的常用筛选字段独立成列，完整调查轮次、工具调用、Execution Intent、证据与 Workflow 历史保存在 PostgreSQL `JSONB` 中；SQLite 测试环境使用等价的 JSON `TEXT`。

Action Execution 使用数据库租约决定 Worker 所有权，状态为 `pending/running/unknown/succeeded/failed`。同一 Execution Intent 通过序号和活动状态唯一索引严格串行；租约过期后可以由其他 Worker 接管，但必须复用原 `idempotency_key`。
异步 Operation 会持久化下次轮询时间和总截止时间；Worker 重启后可以继续查询原 Operation。

正式运行固定使用 PostgreSQL：

```bash
psql "$DATABASE_DSN" -v ON_ERROR_STOP=1 \
  -f docs/sql/postgresql/001_create_approval_requests.up.sql
psql "$DATABASE_DSN" -v ON_ERROR_STOP=1 \
  -f docs/sql/postgresql/002_create_action_executions.up.sql
psql "$DATABASE_DSN" -v ON_ERROR_STOP=1 \
  -f docs/sql/postgresql/003_add_action_polling_schedule.up.sql
psql "$DATABASE_DSN" -v ON_ERROR_STOP=1 \
  -f docs/sql/postgresql/004_create_run_artifacts.up.sql
psql "$DATABASE_DSN" -v ON_ERROR_STOP=1 \
  -f docs/sql/postgresql/005_rename_plan_to_execution_intent.up.sql
psql "$DATABASE_DSN" -v ON_ERROR_STOP=1 \
  -f docs/sql/postgresql/006_create_run_checkpoints.up.sql
```

需要回滚本次建表时：

```bash
psql "$DATABASE_DSN" -v ON_ERROR_STOP=1 \
  -f docs/sql/postgresql/006_create_run_checkpoints.down.sql
psql "$DATABASE_DSN" -v ON_ERROR_STOP=1 \
  -f docs/sql/postgresql/005_rename_plan_to_execution_intent.down.sql
psql "$DATABASE_DSN" -v ON_ERROR_STOP=1 \
  -f docs/sql/postgresql/004_create_run_artifacts.down.sql
psql "$DATABASE_DSN" -v ON_ERROR_STOP=1 \
  -f docs/sql/postgresql/003_add_action_polling_schedule.down.sql
psql "$DATABASE_DSN" -v ON_ERROR_STOP=1 \
  -f docs/sql/postgresql/002_create_action_executions.down.sql
psql "$DATABASE_DSN" -v ON_ERROR_STOP=1 \
  -f docs/sql/postgresql/001_create_approval_requests.down.sql
```

运行时配置：

```env
DATABASE_DSN=postgres://talon:password@localhost:5432/talon?sslmode=disable
```

运行时不提供数据库类型选择，也不会自动建表；缺少 `DATABASE_DSN` 会直接报错。SQLite 及 `docs/sql/sqlite` 脚本仅用于 Go 自动化测试。

按终端输出的 `run_id` 查询一次运行：

```sql
SELECT run_id, scenario_id, outcome, stop_reason, total_tokens,
       failure_stage, started_at, finished_at
FROM run_artifacts
WHERE run_id = '00000000-0000-4000-8000-000000000000';

SELECT artifact -> 'agent_runs' AS agent_runs
FROM run_artifacts
WHERE run_id = '00000000-0000-4000-8000-000000000000';
```

`run_checkpoints` 每个运行保存一条最新检查点，`revision` 用于条件更新，`updated_at_unix_ns` 是 UTC Unix 纳秒时间。等待审批的检查点与审批请求原子提交。检查点目前只覆盖创建、意图接受和等待审批，不代表运行最终状态，也尚不支持直接续跑。

```sql
SELECT run_id, revision, schema_version,
       payload ->> 'boundary' AS boundary,
       payload -> 'workflow' AS workflow
FROM run_checkpoints
WHERE run_id = '00000000-0000-4000-8000-000000000000';
```

运行 PostgreSQL Store 契约测试（使用专用测试数据库，测试会建表和写入数据）：

```bash
TALON_TEST_POSTGRES_DSN="$DATABASE_DSN" go test ./internal/storage -run Postgres
```

### 检查点写入契约

- `checkpoint.Data.Validate` 同时用于存储读写入口；`SaveWithApprovals` 还检查请求集合与当前冻结动作、digest、DryRun 和风险策略的绑定。调整 Go 包和内部方法名没有更改字段、JSON 名称、schema_version 或业务状态，不增加迁移。
- `Save` 拒绝 `awaiting_approval`；该边界只能由 `SaveWithApprovals` 在一个事务中创建审批请求并条件写入检查点。任何审批错误、检查点错误或 revision 冲突都会回滚，不留下孤立审批单。
- `revision` 是每个 run 的存储写入版本，独立于 `Workflow.Version`。相同预期 revision 和相同内容的提交重试返回原记录；旧版本不同内容不能覆盖新版本。
- PostgreSQL 重试用 JSONB 等值判断，避免数字记法规范化造成伪冲突；解码检查点使用 `json.Number` 保留大整数。SQLite 的 JSON TEXT 测试不能代替 PostgreSQL 契约验证。
- PostgreSQL 契约测试仅使用显式的 `TALON_TEST_POSTGRES_DSN` 测试实例；未设置时跳过。不要将正式数据库连接填入该变量。测试会创建表并写入测试数据。

运行检查点与审计的触发者和收尾顺序见 [运行生命周期与持久化边界](../run-lifecycle.md)。
