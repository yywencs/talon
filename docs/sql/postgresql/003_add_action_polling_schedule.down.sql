BEGIN;

DROP INDEX IF EXISTS idx_action_executions_next;
CREATE INDEX idx_action_executions_next
    ON action_executions(plan_id, sequence_no, status);

ALTER TABLE action_executions
    DROP COLUMN IF EXISTS operation_deadline_unix_ns,
    DROP COLUMN IF EXISTS next_poll_at_unix_ns;

COMMIT;
