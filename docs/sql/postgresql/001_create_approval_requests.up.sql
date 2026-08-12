BEGIN;

CREATE TABLE IF NOT EXISTS approval_requests (
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
);

CREATE INDEX IF NOT EXISTS idx_approval_requests_pending
    ON approval_requests(requested_at_unix_ns, id)
    WHERE status = 'pending';

COMMIT;
