BEGIN;

CREATE TABLE IF NOT EXISTS run_artifacts (
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
);

CREATE INDEX IF NOT EXISTS idx_run_artifacts_scenario_started
    ON run_artifacts(scenario_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_run_artifacts_outcome_started
    ON run_artifacts(outcome, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_run_artifacts_failure_stage
    ON run_artifacts(failure_stage) WHERE failure_stage <> '';
CREATE INDEX IF NOT EXISTS idx_run_artifacts_artifact_gin
    ON run_artifacts USING GIN (artifact jsonb_path_ops);

COMMIT;
