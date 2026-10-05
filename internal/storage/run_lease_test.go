package storage

import (
	"context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/checkpoint"
	"github.com/wen/opentalon/internal/execution"
	"github.com/wen/opentalon/internal/runartifact"
	"github.com/wen/opentalon/internal/runlease"
	"github.com/wen/opentalon/internal/runmeta"
	"github.com/wen/opentalon/internal/workflow"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRunLeaseTakeoverFencesCheckpointWrites(t *testing.T) {
	ctx := context.Background()
	db, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "lease.db"))
	require.NoError(t, err)
	defer db.Close()
	token, err := db.RunLeases().Acquire(ctx, "run", "first", time.Minute)
	require.NoError(t, err)
	_, err = db.RunLeases().Acquire(ctx, "run", "second", time.Minute)
	require.ErrorIs(t, err, runlease.ErrBusy)
	flow, err := workflow.NewIncidentWorkflow(workflow.Config{IncidentID: "incident", IntentIDPrefix: "run"})
	require.NoError(t, err)
	data := checkpoint.Data{SchemaVersion: checkpoint.SchemaVersion, RunID: "run", Boundary: checkpoint.Created, Workflow: flow.Snapshot()}
	first := db.OwnedCheckpoints(token)
	_, err = first.Save(ctx, data, 0)
	require.NoError(t, err)
	// Deterministically simulate expiration without a wall-clock sleep.
	_, err = db.db.Exec(`UPDATE run_leases SET lease_until_unix_ns=0 WHERE run_id='run'`)
	require.NoError(t, err)
	newer, err := db.RunLeases().Acquire(ctx, "run", "second", time.Minute)
	require.NoError(t, err)
	assert.Greater(t, newer.Epoch, token.Epoch)
	require.ErrorIs(t, db.RunLeases().Renew(ctx, token, time.Minute), runlease.ErrLost)
	require.ErrorIs(t, db.RunLeases().Release(ctx, token), runlease.ErrLost)
	_, err = first.Save(ctx, data, 1)
	require.ErrorIs(t, err, runlease.ErrLost)
	_, err = db.OwnedCheckpoints(newer).Save(ctx, data, 1)
	require.NoError(t, err)
}
func TestConcurrentRunClaimsHaveOneWinner(t *testing.T) {
	ctx := context.Background()
	db, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "lease.db"))
	require.NoError(t, err)
	defer db.Close()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, owner := range []string{"one", "two"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			_, err := db.RunLeases().Acquire(ctx, "run", owner, time.Minute)
			results <- err
		}(owner)
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else {
			assert.ErrorIs(t, err, runlease.ErrBusy)
		}
	}
	assert.Equal(t, 1, wins)
}

func TestLostRunOwnerCannotWriteExecutionOrAudit(t *testing.T) {
	ctx := context.Background()
	db, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "lease.db"))
	require.NoError(t, err)
	defer db.Close()
	token, err := db.RunLeases().Acquire(ctx, "run", "first", time.Minute)
	require.NoError(t, err)
	owned := db.OwnedRun(token)
	spec := execution.Spec{IncidentID: "incident", IntentID: "run-intent-1", ActionID: "run-intent-1-action-1", ActionDigest: "digest", Sequence: 1, ToolName: "fix", IdempotencyKey: "stable-key"}
	_, err = owned.Executions().Prepare(ctx, []execution.Spec{spec})
	require.NoError(t, err)
	_, err = owned.Executions().ClaimNext(ctx, execution.Claim{IntentID: spec.IntentID, OwnerID: "old", LeaseDuration: time.Minute})
	require.NoError(t, err)
	_, err = db.db.Exec(`UPDATE run_leases SET lease_until_unix_ns=0 WHERE run_id='run'`)
	require.NoError(t, err)
	_, err = db.RunLeases().Acquire(ctx, "run", "second", time.Minute)
	require.NoError(t, err)
	_, err = owned.Executions().Complete(ctx, spec.ActionID, "old", "operation", "succeeded", execution.StatusSucceeded, "")
	require.ErrorIs(t, err, runlease.ErrLost)
	_, err = owned.Executions().Prepare(ctx, []execution.Spec{spec})
	require.ErrorIs(t, err, runlease.ErrLost)
	artifact := runartifact.New("incident", runmeta.Provenance{CodeVersion: "test", DatasetVersion: "test"}, runmeta.Config{}).Snapshot()
	artifact.RunID = "run"
	require.ErrorIs(t, owned.RunArtifacts().Upsert(ctx, artifact), runlease.ErrLost)
	row, err := db.Executions().Get(ctx, spec.ActionID)
	require.NoError(t, err)
	assert.Equal(t, execution.StatusRunning, row.Status)
}
