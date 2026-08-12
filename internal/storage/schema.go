package storage

import (
	"context"
	"database/sql"
	"fmt"
)

var sqliteSchema = []string{
	`CREATE TABLE IF NOT EXISTS approval_requests (
    id                    TEXT PRIMARY KEY,
    incident_id           TEXT NOT NULL,
    plan_id               TEXT NOT NULL,
    action_id             TEXT NOT NULL,
    action_digest         TEXT NOT NULL,
    dry_run_operation_id  TEXT NOT NULL,
    tool_name             TEXT NOT NULL,
    arguments_json        TEXT NOT NULL,
    risk                  TEXT NOT NULL,
    policy_reason         TEXT NOT NULL DEFAULT '',
    status                TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'rejected')),
    requested_at_unix_ns  INTEGER NOT NULL,
    decided_at_unix_ns    INTEGER,
    decided_by            TEXT NOT NULL DEFAULT '',
    decision_reason       TEXT NOT NULL DEFAULT '',
    UNIQUE (plan_id, action_id)
)`,
	`CREATE INDEX IF NOT EXISTS idx_approval_requests_pending
    ON approval_requests(status, requested_at_unix_ns, id)`,
}

var postgresSchema = []string{
	`CREATE TABLE IF NOT EXISTS approval_requests (
    id                    TEXT PRIMARY KEY,
    incident_id           TEXT NOT NULL,
    plan_id               TEXT NOT NULL,
    action_id             TEXT NOT NULL,
    action_digest         TEXT NOT NULL,
    dry_run_operation_id  TEXT NOT NULL,
    tool_name             TEXT NOT NULL,
    arguments_json        JSONB NOT NULL,
    risk                  TEXT NOT NULL,
    policy_reason         TEXT NOT NULL DEFAULT '',
    status                TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'rejected')),
    requested_at_unix_ns  BIGINT NOT NULL,
    decided_at_unix_ns    BIGINT,
    decided_by            TEXT NOT NULL DEFAULT '',
    decision_reason       TEXT NOT NULL DEFAULT '',
    UNIQUE (plan_id, action_id)
)`,
	`CREATE INDEX IF NOT EXISTS idx_approval_requests_pending
    ON approval_requests(requested_at_unix_ns, id)
    WHERE status = 'pending'`,
}

func migrate(ctx context.Context, db *sql.DB, driver Driver) error {
	statements := sqliteSchema
	if driver == DriverPostgres {
		statements = postgresSchema
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin storage migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate %s storage: %w", driver, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %s storage migration: %w", driver, err)
	}
	return nil
}
