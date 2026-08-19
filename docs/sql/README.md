# Talon 数据库脚本

这里保存 Talon 控制面数据库的建表和回滚脚本。目前包含 Action 审批、Action Execution 和结构化 RunArtifact。RunArtifact 的常用筛选字段独立成列，完整调查轮次、工具调用、Execution Intent、证据与 Workflow 历史保存在 PostgreSQL `JSONB` 中；SQLite 测试环境使用等价的 JSON `TEXT`。

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
```

需要回滚本次建表时：

```bash
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

运行 PostgreSQL Store 契约测试：

```bash
TALON_TEST_POSTGRES_DSN="$DATABASE_DSN" go test ./internal/storage -run Postgres
```
