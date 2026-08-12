# Talon 数据库脚本

这里保存 Talon 控制面数据库的建表和回滚脚本。目前包含 Action 审批和 Action Execution 记录，后续 Workflow 和 Operation 记录也按版本继续追加。

Action Execution 使用数据库租约决定 Worker 所有权，状态为 `pending/running/unknown/succeeded/failed`。同一 Plan 通过序号和活动状态唯一索引严格串行；租约过期后可以由其他 Worker 接管，但必须复用原 `idempotency_key`。
异步 Operation 会持久化下次轮询时间和总截止时间；Worker 重启后可以继续查询原 Operation。

正式运行推荐 PostgreSQL：

```bash
psql "$DATABASE_DSN" -v ON_ERROR_STOP=1 \
  -f docs/sql/postgresql/001_create_approval_requests.up.sql
psql "$DATABASE_DSN" -v ON_ERROR_STOP=1 \
  -f docs/sql/postgresql/002_create_action_executions.up.sql
psql "$DATABASE_DSN" -v ON_ERROR_STOP=1 \
  -f docs/sql/postgresql/003_add_action_polling_schedule.up.sql
```

需要回滚本次建表时：

```bash
psql "$DATABASE_DSN" -v ON_ERROR_STOP=1 \
  -f docs/sql/postgresql/003_add_action_polling_schedule.down.sql
psql "$DATABASE_DSN" -v ON_ERROR_STOP=1 \
  -f docs/sql/postgresql/002_create_action_executions.down.sql
psql "$DATABASE_DSN" -v ON_ERROR_STOP=1 \
  -f docs/sql/postgresql/001_create_approval_requests.down.sql
```

本地 SQLite 默认由程序自动迁移，也可以手工执行：

```bash
sqlite3 .opentalon/talon.db < docs/sql/sqlite/001_create_approval_requests.up.sql
sqlite3 .opentalon/talon.db < docs/sql/sqlite/002_create_action_executions.up.sql
sqlite3 .opentalon/talon.db < docs/sql/sqlite/003_add_action_polling_schedule.up.sql
```

运行时配置：

```env
DATABASE_DRIVER=postgres
DATABASE_DSN=postgres://talon:password@localhost:5432/talon?sslmode=disable
DATABASE_AUTO_MIGRATE=false
```

`DATABASE_DRIVER` 可取 `sqlite` 或 `postgres`。PostgreSQL 默认关闭自动迁移；集成测试可以显式设置 `DATABASE_AUTO_MIGRATE=true`。

运行 PostgreSQL Store 契约测试：

```bash
TALON_TEST_POSTGRES_DSN="$DATABASE_DSN" go test ./internal/storage -run Postgres
```
