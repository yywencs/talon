package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/wen/opentalon/internal/runartifact"
)

type sqlRunArtifactStore struct {
	db     *sql.DB
	driver Driver
}

var _ runartifact.Store = (*sqlRunArtifactStore)(nil)

func newSQLRunArtifactStore(db *sql.DB, driver Driver) *sqlRunArtifactStore {
	return &sqlRunArtifactStore{db: db, driver: driver}
}

func (s *sqlRunArtifactStore) Upsert(ctx context.Context, artifact runartifact.RunArtifact) error {
	if strings.TrimSpace(artifact.RunID) == "" || strings.TrimSpace(artifact.ScenarioID) == "" ||
		strings.TrimSpace(artifact.ArtifactSchemaVersion) == "" || strings.TrimSpace(artifact.AgentVersion) == "" ||
		strings.TrimSpace(artifact.DatasetVersion) == "" {
		return fmt.Errorf("run ID, scenario ID, artifact schema version, agent version and dataset version are required")
	}
	if artifact.StartedAt.IsZero() || (artifact.Outcome != "running" && artifact.Outcome != "completed" && artifact.Outcome != "failed") {
		return fmt.Errorf("run artifact start time and valid outcome are required")
	}
	payload, err := json.Marshal(artifact)
	if err != nil {
		return fmt.Errorf("marshal run artifact %q: %w", artifact.RunID, err)
	}
	var finishedAt any
	if !artifact.FinishedAt.IsZero() {
		finishedAt = artifact.FinishedAt.UTC()
	}
	failureStage := ""
	if artifact.Failure != nil {
		failureStage = strings.TrimSpace(artifact.Failure.Stage)
	}
	query := `INSERT INTO run_artifacts (
    run_id, scenario_id, schema_version, outcome, stop_reason, started_at,
    finished_at, duration_ms, total_tokens, failure_stage, artifact
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, __ARTIFACT__)
ON CONFLICT(run_id) DO UPDATE SET
    scenario_id = excluded.scenario_id,
    schema_version = excluded.schema_version,
    outcome = excluded.outcome,
    stop_reason = excluded.stop_reason,
    started_at = excluded.started_at,
    finished_at = excluded.finished_at,
    duration_ms = excluded.duration_ms,
    total_tokens = excluded.total_tokens,
    failure_stage = excluded.failure_stage,
    artifact = excluded.artifact,
    updated_at = CURRENT_TIMESTAMP`
	artifactPlaceholder := "?"
	if s.driver == DriverPostgres {
		artifactPlaceholder = "CAST(? AS JSONB)"
	}
	query = strings.Replace(query, "__ARTIFACT__", artifactPlaceholder, 1)
	_, err = s.db.ExecContext(ctx, bindSQL(s.driver, query), artifact.RunID, artifact.ScenarioID, artifact.ArtifactSchemaVersion,
		artifact.Outcome, artifact.StopReason, artifact.StartedAt.UTC(), finishedAt,
		artifact.Duration.Milliseconds(), artifact.Summary.TotalTokens, failureStage, string(payload))
	if err != nil {
		return fmt.Errorf("upsert run artifact %q: %w", artifact.RunID, err)
	}
	return nil
}

func (s *sqlRunArtifactStore) Get(ctx context.Context, runID string) (runartifact.RunArtifact, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return runartifact.RunArtifact{}, fmt.Errorf("run ID is required")
	}
	var payload []byte
	err := s.db.QueryRowContext(ctx, bindSQL(s.driver, `SELECT artifact FROM run_artifacts WHERE run_id = ?`), runID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return runartifact.RunArtifact{}, runartifact.ErrNotFound
	}
	if err != nil {
		return runartifact.RunArtifact{}, fmt.Errorf("get run artifact %q: %w", runID, err)
	}
	var result runartifact.RunArtifact
	if err := json.Unmarshal(payload, &result); err != nil {
		return runartifact.RunArtifact{}, fmt.Errorf("decode run artifact %q: %w", runID, err)
	}
	return result, nil
}
