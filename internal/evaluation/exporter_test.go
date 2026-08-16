package evaluation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/runartifact"
	"github.com/wen/opentalon/internal/storage"
	"github.com/wen/opentalon/internal/workflow"
)

func TestExportBatchSelectsTerminalRunsByCodeAndDatasetVersion(t *testing.T) {
	ctx := context.Background()
	database, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "talon.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })

	selected := completedArtifact("mapping-regression-rollback-001", "code-a", "toolops-v1")
	failed := failedArtifact("mapping-regression-rollback-001", "code-a", "toolops-v1")
	require.NoError(t, database.RunArtifacts().Upsert(ctx, selected))
	require.NoError(t, database.RunArtifacts().Upsert(ctx, failed))
	require.NoError(t, database.RunArtifacts().Upsert(ctx, completedArtifact("mapping-regression-rollback-001", "code-b", "toolops-v1")))
	require.NoError(t, database.RunArtifacts().Upsert(ctx, completedArtifact("mapping-regression-rollback-001", "code-a", "toolops-v2")))
	running := runartifact.New("mapping-regression-rollback-001", runartifact.Provenance{CodeVersion: "code-a", DatasetVersion: "toolops-v1"}, runartifact.RunConfig{}).Snapshot()
	require.NoError(t, database.RunArtifacts().Upsert(ctx, running))

	output := filepath.Join(t.TempDir(), "batch")
	manifest, err := ExportBatch(ctx, ExportConfig{
		Store: database.RunArtifacts(), DataRoot: dataRoot(t), DatasetVersion: "toolops-v1",
		CodeVersion: "code-a", OutputDir: output,
	})
	require.NoError(t, err)
	require.Len(t, manifest.Runs, 2)
	outcomes := map[string]string{}
	for _, run := range manifest.Runs {
		outcomes[run.RunID] = run.Outcome
	}
	assert.Equal(t, "completed", outcomes[selected.RunID])
	assert.Equal(t, "failed", outcomes[failed.RunID])

	var input Input
	readJSON(t, filepath.Join(output, selected.RunID+".json"), &input)
	assert.Equal(t, InputSchemaVersion, input.SchemaVersion)
	assert.Equal(t, selected, input.Artifact)
	assert.Equal(t, selected.ScenarioID, input.Expectations.ScenarioID)
	assert.Equal(t, "toolops-expectation/v1.1", input.Expectations.SchemaVersion)

	var persistedManifest Manifest
	readJSON(t, filepath.Join(output, manifestFileName), &persistedManifest)
	assert.Equal(t, manifest, persistedManifest)
}

func TestExportBatchRejectsMissingCohortAndExistingOutput(t *testing.T) {
	ctx := context.Background()
	database, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "talon.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	config := ExportConfig{
		Store: database.RunArtifacts(), DataRoot: dataRoot(t), DatasetVersion: "toolops-v1",
		CodeVersion: "missing", OutputDir: filepath.Join(t.TempDir(), "batch"),
	}
	_, err = ExportBatch(ctx, config)
	require.ErrorIs(t, err, ErrNoArtifacts)

	artifact := completedArtifact("mapping-regression-rollback-001", "code-a", "toolops-v1")
	require.NoError(t, database.RunArtifacts().Upsert(ctx, artifact))
	config.CodeVersion = "code-a"
	require.NoError(t, os.Mkdir(config.OutputDir, 0o700))
	_, err = ExportBatch(ctx, config)
	require.ErrorContains(t, err, "already exists")
}

func completedArtifact(scenarioID, codeVersion, datasetVersion string) runartifact.RunArtifact {
	recorder := runartifact.New(scenarioID, runartifact.Provenance{CodeVersion: codeVersion, DatasetVersion: datasetVersion}, runartifact.RunConfig{})
	return recorder.Finish("resolved", workflow.Snapshot{State: workflow.StateResolved}, nil)
}

func failedArtifact(scenarioID, codeVersion, datasetVersion string) runartifact.RunArtifact {
	recorder := runartifact.New(scenarioID, runartifact.Provenance{CodeVersion: codeVersion, DatasetVersion: datasetVersion}, runartifact.RunConfig{})
	return recorder.Finish("", workflow.Snapshot{State: workflow.StateInvestigating}, assert.AnError)
}

func dataRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", "data"))
}

func readJSON(t *testing.T, path string, target any) {
	t.Helper()
	payload, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(payload, target))
}
