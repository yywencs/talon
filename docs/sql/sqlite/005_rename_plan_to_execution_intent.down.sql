BEGIN;

DROP INDEX IF EXISTS idx_action_executions_next;
DROP INDEX IF EXISTS idx_action_executions_one_active_intent;

ALTER TABLE approval_requests RENAME COLUMN intent_id TO plan_id;
ALTER TABLE action_executions RENAME COLUMN intent_id TO plan_id;

CREATE INDEX idx_action_executions_next
    ON action_executions(plan_id, next_poll_at_unix_ns, sequence_no, status);
CREATE UNIQUE INDEX idx_action_executions_one_active_plan
    ON action_executions(plan_id)
    WHERE status IN ('running', 'unknown');

COMMIT;
