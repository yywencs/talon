# Talon 数据库脚本

这里保存 Talon 控制面数据库的建表和回滚脚本。目前包含 Action 审批记录，后续 Workflow、Action Execution 和 Operation 记录也按版本继续追加。

正式运行推荐 PostgreSQL：

```bash
psql "$DATABASE_DSN" -v ON_ERROR_STOP=1 \
  -f docs/sql/postgresql/001_create_approval_requests.up.sql
```

需要回滚本次建表时：

```bash
psql "$DATABASE_DSN" -v ON_ERROR_STOP=1 \
  -f docs/sql/postgresql/001_create_approval_requests.down.sql
```

本地 SQLite 默认由程序自动迁移，也可以手工执行：

```bash
sqlite3 .opentalon/talon.db < docs/sql/sqlite/001_create_approval_requests.up.sql
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
