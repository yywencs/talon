-- Immutable candidates, evidence snapshots, release manifests and validation reports.
-- Only the active pointer is mutable, using optimistic revision checks.
CREATE TABLE IF NOT EXISTS experience_objects (
    kind TEXT NOT NULL,
    object_key TEXT NOT NULL,
    revision BIGINT NOT NULL CHECK (revision > 0),
    payload TEXT NOT NULL,
    PRIMARY KEY(kind, object_key)
);
