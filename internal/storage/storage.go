// Package storage 统一管理 Talon 控制面数据库及各领域 Store。
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/wen/opentalon/internal/approval"
	"github.com/wen/opentalon/internal/execution"
	"github.com/wen/opentalon/internal/runartifact"
	_ "modernc.org/sqlite"
)

const EnvDatabaseDSN = "DATABASE_DSN"

// Driver 是 storage 支持的数据库类型。
type Driver string

const (
	DriverSQLite   Driver = "sqlite"
	DriverPostgres Driver = "postgres"
)

// Config 描述数据库连接与迁移策略。
type Config struct {
	Driver          Driver
	DSN             string
	AutoMigrate     bool
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// Storage 持有共享连接池，并向各领域暴露窄接口 Store。
type Storage struct {
	db           *sql.DB
	approvals    approval.Store
	executions   execution.Store
	runArtifacts runartifact.Store
}

// LoadPostgresConfigFromEnv 读取正式运行的 PostgreSQL 配置。
// 表结构不会自动创建，应先执行 docs/sql/postgresql 中的迁移。
func LoadPostgresConfigFromEnv() (Config, error) {
	dsn := strings.TrimSpace(os.Getenv(EnvDatabaseDSN))
	if dsn == "" {
		return Config{}, fmt.Errorf("%s is required for PostgreSQL", EnvDatabaseDSN)
	}
	return Config{
		Driver: DriverPostgres, DSN: dsn,
		MaxOpenConns: 10, MaxIdleConns: 5, ConnMaxLifetime: 30 * time.Minute,
	}, nil
}

// OpenPostgresFromEnv 使用 DATABASE_DSN 打开正式运行的 PostgreSQL。
func OpenPostgresFromEnv(ctx context.Context) (*Storage, error) {
	config, err := LoadPostgresConfigFromEnv()
	if err != nil {
		return nil, err
	}
	return Open(ctx, config)
}

// OpenSQLite 打开并自动初始化 SQLite，仅供测试使用。
func OpenSQLite(ctx context.Context, path string) (*Storage, error) {
	return Open(ctx, Config{Driver: DriverSQLite, DSN: path, AutoMigrate: true, MaxOpenConns: 1, MaxIdleConns: 1})
}

// OpenPostgres 打开 PostgreSQL；表结构默认由 docs/sql/postgresql 迁移管理。
func OpenPostgres(ctx context.Context, dsn string) (*Storage, error) {
	return Open(ctx, Config{Driver: DriverPostgres, DSN: dsn, MaxOpenConns: 10, MaxIdleConns: 5, ConnMaxLifetime: 30 * time.Minute})
}

// Open 创建连接池、检查连接并初始化领域 Store。
func Open(ctx context.Context, config Config) (*Storage, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	if config.Driver == DriverSQLite && config.DSN != ":memory:" && !strings.HasPrefix(config.DSN, "file:") {
		if err := os.MkdirAll(filepath.Dir(config.DSN), 0o755); err != nil {
			return nil, fmt.Errorf("create SQLite database directory: %w", err)
		}
	}
	driverName := "sqlite"
	if config.Driver == DriverPostgres {
		driverName = "pgx"
	}
	db, err := sql.Open(driverName, config.DSN)
	if err != nil {
		return nil, fmt.Errorf("open %s database: %w", config.Driver, err)
	}
	if config.MaxOpenConns > 0 {
		db.SetMaxOpenConns(config.MaxOpenConns)
	}
	if config.MaxIdleConns >= 0 {
		db.SetMaxIdleConns(config.MaxIdleConns)
	}
	if config.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(config.ConnMaxLifetime)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping %s database: %w", config.Driver, err)
	}
	if config.AutoMigrate {
		if err := migrate(ctx, db, config.Driver); err != nil {
			_ = db.Close()
			return nil, err
		}
	} else if err := validateSchema(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	store := &Storage{db: db}
	store.approvals = newSQLApprovalStore(db, config.Driver)
	store.executions = newSQLExecutionStore(db, config.Driver)
	store.runArtifacts = newSQLRunArtifactStore(db, config.Driver)
	return store, nil
}

// RunArtifacts 返回共享连接池上的结构化运行审计 Store。
func (s *Storage) RunArtifacts() runartifact.Store {
	if s == nil {
		return nil
	}
	return s.runArtifacts
}

// Executions 返回共享连接池上的 Action 执行 Store。
func (s *Storage) Executions() execution.Store {
	if s == nil {
		return nil
	}
	return s.executions
}

// Approvals 返回共享连接池上的审批 Store。
func (s *Storage) Approvals() approval.Store {
	if s == nil {
		return nil
	}
	return s.approvals
}

// Close 关闭共享数据库连接池。
func (s *Storage) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func validateConfig(config Config) error {
	if !config.Driver.valid() {
		return fmt.Errorf("unsupported database driver %q", config.Driver)
	}
	if strings.TrimSpace(config.DSN) == "" {
		return fmt.Errorf("database DSN is required")
	}
	if config.MaxOpenConns < 0 || config.MaxIdleConns < 0 {
		return fmt.Errorf("database connection limits must not be negative")
	}
	return nil
}

func (d Driver) valid() bool {
	return d == DriverSQLite || d == DriverPostgres
}

func validateSchema(ctx context.Context, db *sql.DB) error {
	checks := map[string]string{
		"approval_requests": `SELECT intent_id FROM approval_requests WHERE 1 = 0`,
		"action_executions": `SELECT intent_id, next_poll_at_unix_ns, operation_deadline_unix_ns FROM action_executions WHERE 1 = 0`,
		"run_artifacts":     `SELECT run_id, artifact FROM run_artifacts WHERE 1 = 0`,
	}
	for table, query := range checks {
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("validate storage schema table %s: %w; run the SQL migrations under docs/sql", table, err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close storage schema validation: %w", err)
		}
	}
	return nil
}
