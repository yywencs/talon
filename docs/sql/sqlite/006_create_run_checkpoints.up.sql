BEGIN;
CREATE TABLE IF NOT EXISTS run_checkpoints (
    run_id TEXT PRIMARY KEY,
    schema_version TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    payload TEXT NOT NULL,
    updated_at_unix_ns INTEGER NOT NULL
);
COMMIT;
