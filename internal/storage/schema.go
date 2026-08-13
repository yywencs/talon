package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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
	`CREATE TABLE IF NOT EXISTS action_executions (
    action_id             TEXT PRIMARY KEY,
    incident_id           TEXT NOT NULL,
    plan_id               TEXT NOT NULL,
    action_digest         TEXT NOT NULL,
    sequence_no           INTEGER NOT NULL CHECK (sequence_no > 0),
    tool_name             TEXT NOT NULL,
    idempotency_key       TEXT NOT NULL UNIQUE,
    status                TEXT NOT NULL CHECK (status IN ('pending', 'running', 'unknown', 'succeeded', 'failed')),
    owner_id              TEXT NOT NULL DEFAULT '',
    lease_until_unix_ns   INTEGER,
    next_poll_at_unix_ns  INTEGER,
    operation_deadline_unix_ns INTEGER,
    attempt               INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    operation_id          TEXT NOT NULL DEFAULT '',
    operation_status      TEXT NOT NULL DEFAULT '',
    last_error            TEXT NOT NULL DEFAULT '',
    created_at_unix_ns    INTEGER NOT NULL,
    updated_at_unix_ns    INTEGER NOT NULL,
    finished_at_unix_ns   INTEGER,
    UNIQUE (plan_id, sequence_no)
)`,
	`ALTER TABLE action_executions ADD COLUMN next_poll_at_unix_ns INTEGER`,
	`ALTER TABLE action_executions ADD COLUMN operation_deadline_unix_ns INTEGER`,
	`DROP INDEX IF EXISTS idx_action_executions_next`,
	`CREATE INDEX idx_action_executions_next
    ON action_executions(plan_id, next_poll_at_unix_ns, sequence_no, status)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_action_executions_one_active_plan
    ON action_executions(plan_id)
    WHERE status IN ('running', 'unknown')`,
	`CREATE TABLE IF NOT EXISTS run_artifacts (
    run_id          TEXT PRIMARY KEY,
    scenario_id     TEXT NOT NULL,
    schema_version  TEXT NOT NULL,
    outcome         TEXT NOT NULL CHECK (outcome IN ('running', 'completed', 'failed')),
    stop_reason     TEXT NOT NULL DEFAULT '',
    started_at      TEXT NOT NULL,
    finished_at     TEXT,
    duration_ms     INTEGER NOT NULL DEFAULT 0,
    total_tokens    INTEGER NOT NULL DEFAULT 0,
    failure_stage   TEXT NOT NULL DEFAULT '',
    artifact        TEXT NOT NULL,
    created_at      TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
)`,
	`CREATE INDEX IF NOT EXISTS idx_run_artifacts_scenario_started ON run_artifacts(scenario_id, started_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_run_artifacts_outcome_started ON run_artifacts(outcome, started_at DESC)`,
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
	`CREATE TABLE IF NOT EXISTS action_executions (
    action_id             TEXT PRIMARY KEY,
    incident_id           TEXT NOT NULL,
    plan_id               TEXT NOT NULL,
    action_digest         TEXT NOT NULL,
    sequence_no           INTEGER NOT NULL CHECK (sequence_no > 0),
    tool_name             TEXT NOT NULL,
    idempotency_key       TEXT NOT NULL UNIQUE,
    status                TEXT NOT NULL CHECK (status IN ('pending', 'running', 'unknown', 'succeeded', 'failed')),
    owner_id              TEXT NOT NULL DEFAULT '',
    lease_until_unix_ns   BIGINT,
    next_poll_at_unix_ns  BIGINT,
    operation_deadline_unix_ns BIGINT,
    attempt               INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    operation_id          TEXT NOT NULL DEFAULT '',
    operation_status      TEXT NOT NULL DEFAULT '',
    last_error            TEXT NOT NULL DEFAULT '',
    created_at_unix_ns    BIGINT NOT NULL,
    updated_at_unix_ns    BIGINT NOT NULL,
    finished_at_unix_ns   BIGINT,
    UNIQUE (plan_id, sequence_no)
)`,
	`ALTER TABLE action_executions ADD COLUMN IF NOT EXISTS next_poll_at_unix_ns BIGINT`,
	`ALTER TABLE action_executions ADD COLUMN IF NOT EXISTS operation_deadline_unix_ns BIGINT`,
	`DROP INDEX IF EXISTS idx_action_executions_next`,
	`CREATE INDEX idx_action_executions_next
    ON action_executions(plan_id, next_poll_at_unix_ns, sequence_no, status)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_action_executions_one_active_plan
    ON action_executions(plan_id)
    WHERE status IN ('running', 'unknown')`,
	`CREATE TABLE IF NOT EXISTS run_artifacts (
    run_id          UUID PRIMARY KEY,
    scenario_id     TEXT NOT NULL,
    schema_version  TEXT NOT NULL,
    outcome         TEXT NOT NULL CHECK (outcome IN ('running', 'completed', 'failed')),
    stop_reason     TEXT NOT NULL DEFAULT '',
    started_at      TIMESTAMPTZ NOT NULL,
    finished_at     TIMESTAMPTZ,
    duration_ms     BIGINT NOT NULL DEFAULT 0,
    total_tokens    BIGINT NOT NULL DEFAULT 0,
    failure_stage   TEXT NOT NULL DEFAULT '',
    artifact        JSONB NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
)`,
	`CREATE INDEX IF NOT EXISTS idx_run_artifacts_scenario_started ON run_artifacts(scenario_id, started_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_run_artifacts_outcome_started ON run_artifacts(outcome, started_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_run_artifacts_failure_stage ON run_artifacts(failure_stage) WHERE failure_stage <> ''`,
	`CREATE INDEX IF NOT EXISTS idx_run_artifacts_artifact_gin ON run_artifacts USING GIN (artifact jsonb_path_ops)`,
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
			if driver == DriverSQLite && strings.HasPrefix(statement, "ALTER TABLE action_executions ADD COLUMN") &&
				strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
				continue
			}
			return fmt.Errorf("migrate %s storage: %w", driver, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %s storage migration: %w", driver, err)
	}
	return nil
}
