package runartifact

import (
	"context"
	"errors"
)

var ErrNotFound = errors.New("run artifact not found")

// Store persists complete JSON snapshots. Upsert is used for running checkpoints and finalization.
type Store interface {
	Upsert(ctx context.Context, artifact RunArtifact) error
	Get(ctx context.Context, runID string) (RunArtifact, error)
}
