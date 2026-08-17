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
		strings.TrimSpace(artifact.SchemaVersion) == "" || strings.TrimSpace(artifact.Provenance.CodeVersion) == "" ||
		strings.TrimSpace(artifact.Provenance.DatasetVersion) == "" {
		return fmt.Errorf("run ID, scenario ID, artifact schema version, code version and dataset version are required")
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
	_, err = s.db.ExecContext(ctx, bindSQL(s.driver, query), artifact.RunID, artifact.ScenarioID, artifact.SchemaVersion,
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
	return runartifact.Normalize(result), nil
}

func (s *sqlRunArtifactStore) List(ctx context.Context, filter runartifact.VersionFilter) ([]runartifact.RunArtifact, error) {
	filter.SchemaVersion = strings.TrimSpace(filter.SchemaVersion)
	filter.CodeVersion = strings.TrimSpace(filter.CodeVersion)
	filter.DatasetVersion = strings.TrimSpace(filter.DatasetVersion)
	filter.Outcome = strings.TrimSpace(filter.Outcome)
	if filter.SchemaVersion == "" || filter.CodeVersion == "" || filter.DatasetVersion == "" || filter.Outcome == "" {
		return nil, fmt.Errorf("schema, code, dataset and outcome filters are required")
	}
	codeExpression := "json_extract(artifact, '$.provenance.code_version')"
	datasetExpression := "json_extract(artifact, '$.provenance.dataset_version')"
	if s.driver == DriverPostgres {
		codeExpression = "artifact #>> '{provenance,code_version}'"
		datasetExpression = "artifact #>> '{provenance,dataset_version}'"
	}
	query := fmt.Sprintf(`SELECT artifact FROM run_artifacts
WHERE schema_version = ? AND outcome = ?
  AND %s = ? AND %s = ?
ORDER BY started_at ASC, run_id ASC`, codeExpression, datasetExpression)
	rows, err := s.db.QueryContext(ctx, bindSQL(s.driver, query), filter.SchemaVersion, filter.Outcome, filter.CodeVersion, filter.DatasetVersion)
	if err != nil {
		return nil, fmt.Errorf("list run artifacts by version: %w", err)
	}
	defer rows.Close()
	result := make([]runartifact.RunArtifact, 0)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("scan run artifact: %w", err)
		}
		var artifact runartifact.RunArtifact
		if err := json.Unmarshal(payload, &artifact); err != nil {
			return nil, fmt.Errorf("decode run artifact: %w", err)
		}
		result = append(result, runartifact.Normalize(artifact))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate run artifacts: %w", err)
	}
	return result, nil
}
