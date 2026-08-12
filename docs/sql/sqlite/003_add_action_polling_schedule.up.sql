BEGIN;

ALTER TABLE action_executions ADD COLUMN next_poll_at_unix_ns INTEGER;
ALTER TABLE action_executions ADD COLUMN operation_deadline_unix_ns INTEGER;

DROP INDEX IF EXISTS idx_action_executions_next;
CREATE INDEX idx_action_executions_next
    ON action_executions(plan_id, next_poll_at_unix_ns, sequence_no, status);

COMMIT;
