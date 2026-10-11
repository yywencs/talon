package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/experience"
	"github.com/wen/opentalon/internal/runartifact"
	"github.com/wen/opentalon/internal/runmeta"
	"github.com/wen/opentalon/internal/storage"
	"github.com/wen/opentalon/internal/workflow"
)

func snapshotFixture(t *testing.T, outcome string) (string, *storage.Storage, runartifact.RunArtifact) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "run.sqlite")
	db, e := storage.OpenSQLite(context.Background(), path)
	require.NoError(t, e)
	t.Cleanup(func() { db.Close() })
	a := runartifact.RunArtifact{SchemaVersion: runartifact.SchemaVersion, RunID: "snapshot-source", ScenarioID: "mapping-regression-rollback-001", Provenance: runmeta.Provenance{CodeVersion: "test", DatasetVersion: "toolops-v1"}, StartedAt: time.Now().UTC(), Outcome: outcome, AgentRuns: []runartifact.AgentRun{{ToolCalls: []runartifact.ToolCall{{CallID: "obs-1", EvidenceRef: "obs-1", Action: workflow.AgentActionRead, Name: "query_logs", Status: "succeeded", Output: json.RawMessage(`{"data":{"message":"timeout","password":"do-not-retain"}}`)}}}}}
	if outcome != "running" {
		a.FinishedAt = time.Now().UTC()
	}
	require.NoError(t, db.RunArtifacts().Upsert(context.Background(), a))
	stored, e := db.RunArtifacts().Get(context.Background(), a.RunID)
	require.NoError(t, e)
	return path, db, stored
}
func snapshotArgs(path string) []string {
	return []string{"snapshot", "--env-file", "", "--sqlite-test-db", path, "--run-ids", "snapshot-source", "--dataset", "../../data/toolops-v1", "--evaluator-dir", "../../evaluator/src"}
}
func TestSnapshotCommandPersistsEvaluatedMaterialWithoutModels(t *testing.T) {
	path, db, a := snapshotFixture(t, "failed")
	// These invalid settings would fail if this stage initialized an LLM or retrieval service.
	t.Setenv("LLM_PROVIDER", "invalid-for-snapshot-test")
	t.Setenv("QDRANT_URL", "invalid-for-snapshot-test")
	require.NoError(t, run(snapshotArgs(path)))
	require.NoError(t, run(snapshotArgs(path)))
	rows, e := db.Experiences().List(context.Background(), "snapshot")
	require.NoError(t, e)
	require.Len(t, rows, 1)
	var snap experience.Snapshot
	require.NoError(t, json.Unmarshal(rows[0], &snap))
	require.Equal(t, experience.Digest(a), snap.ArtifactDigest)
	require.Equal(t, "failed", snap.Outcome)
	require.NotNil(t, snap.Assessment)
	require.NotEmpty(t, snap.Assessment.Version)
	require.NotEmpty(t, snap.Assessment.ResultDigest)
	require.NotContains(t, string(rows[0]), "do-not-retain")
	require.Contains(t, string(rows[0]), "REDACTED")
	require.NotContains(t, string(rows[0]), `"expectations"`)
	for _, kind := range []string{"candidate", "extraction", "release", "published"} {
		v, e := db.Experiences().List(context.Background(), kind)
		require.NoError(t, e)
		require.Empty(t, v)
	}
	original, e := db.RunArtifacts().Get(context.Background(), a.RunID)
	require.NoError(t, e)
	require.Contains(t, string(original.AgentRuns[0].ToolCalls[0].Output), "do-not-retain")
}
func TestSnapshotRejectsRunningBeforeInvokingEvaluator(t *testing.T) {
	path, db, _ := snapshotFixture(t, "running")
	args := append(snapshotArgs(path), "--python", "/nonexistent-python")
	require.ErrorContains(t, run(args), "only terminal runs")
	rows, e := db.Experiences().List(context.Background(), "snapshot")
	require.NoError(t, e)
	require.Empty(t, rows)
}
func TestSnapshotRequiresRunIDsAndMatchingDataset(t *testing.T) {
	path, db, _ := snapshotFixture(t, "failed")
	require.ErrorContains(t, run(append(snapshotArgs(path), "--run-ids", " , ")), "--run-ids")
	err := run(append(snapshotArgs(path), "--dataset", "../../data/toolops-v2"))
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "dataset does not match"))
	rows, e := db.Experiences().List(context.Background(), "snapshot")
	require.NoError(t, e)
	require.Empty(t, rows)
}
