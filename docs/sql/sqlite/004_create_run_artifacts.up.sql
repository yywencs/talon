BEGIN;

CREATE TABLE IF NOT EXISTS run_artifacts (
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
);

CREATE INDEX IF NOT EXISTS idx_run_artifacts_scenario_started
    ON run_artifacts(scenario_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_run_artifacts_outcome_started
    ON run_artifacts(outcome, started_at DESC);

COMMIT;
