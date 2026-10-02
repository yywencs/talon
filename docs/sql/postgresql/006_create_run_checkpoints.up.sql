BEGIN;
CREATE TABLE IF NOT EXISTS run_checkpoints (
    run_id TEXT PRIMARY KEY,
    schema_version TEXT NOT NULL,
    revision BIGINT NOT NULL CHECK (revision > 0),
    payload JSONB NOT NULL,
    updated_at_unix_ns BIGINT NOT NULL
);
COMMIT;
