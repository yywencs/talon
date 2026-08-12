BEGIN;

CREATE TABLE IF NOT EXISTS action_executions (
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
    attempt               INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    operation_id          TEXT NOT NULL DEFAULT '',
    operation_status      TEXT NOT NULL DEFAULT '',
    last_error            TEXT NOT NULL DEFAULT '',
    created_at_unix_ns    INTEGER NOT NULL,
    updated_at_unix_ns    INTEGER NOT NULL,
    finished_at_unix_ns   INTEGER,
    UNIQUE (plan_id, sequence_no)
);

CREATE INDEX IF NOT EXISTS idx_action_executions_next
    ON action_executions(plan_id, sequence_no, status);

CREATE UNIQUE INDEX IF NOT EXISTS idx_action_executions_one_active_plan
    ON action_executions(plan_id)
    WHERE status IN ('running', 'unknown');

COMMIT;
