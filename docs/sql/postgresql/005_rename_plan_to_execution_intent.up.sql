BEGIN;

ALTER TABLE approval_requests RENAME COLUMN plan_id TO intent_id;
ALTER TABLE action_executions RENAME COLUMN plan_id TO intent_id;

DROP INDEX IF EXISTS idx_action_executions_next;
DROP INDEX IF EXISTS idx_action_executions_one_active_plan;
CREATE INDEX idx_action_executions_next
    ON action_executions(intent_id, next_poll_at_unix_ns, sequence_no, status);
CREATE UNIQUE INDEX idx_action_executions_one_active_intent
    ON action_executions(intent_id)
    WHERE status IN ('running', 'unknown');

COMMIT;
