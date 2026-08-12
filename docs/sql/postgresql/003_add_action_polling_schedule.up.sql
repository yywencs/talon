BEGIN;

ALTER TABLE action_executions
    ADD COLUMN IF NOT EXISTS next_poll_at_unix_ns BIGINT,
    ADD COLUMN IF NOT EXISTS operation_deadline_unix_ns BIGINT;

DROP INDEX IF EXISTS idx_action_executions_next;
CREATE INDEX idx_action_executions_next
    ON action_executions(plan_id, next_poll_at_unix_ns, sequence_no, status);

COMMIT;
