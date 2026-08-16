package runartifact

import (
	"context"
	"errors"
)

var ErrNotFound = errors.New("run artifact not found")

// VersionFilter selects a reproducible cohort of completed runs.
type VersionFilter struct {
	SchemaVersion  string
	CodeVersion    string
	DatasetVersion string
	Outcome        string
}

// Store persists complete JSON snapshots. Upsert is used for running checkpoints and finalization.
type Store interface {
	Upsert(ctx context.Context, artifact RunArtifact) error
	Get(ctx context.Context, runID string) (RunArtifact, error)
	List(ctx context.Context, filter VersionFilter) ([]RunArtifact, error)
}
