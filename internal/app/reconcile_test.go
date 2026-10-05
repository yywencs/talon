package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/approval"
	"github.com/wen/opentalon/internal/checkpoint"
	"github.com/wen/opentalon/internal/controller"
	"github.com/wen/opentalon/internal/storage"
	"github.com/wen/opentalon/internal/workflow"
)

func TestLoadAndReconcileAppliesApprovalAfterDatabaseReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "reconcile.db")
	db, err := storage.OpenSQLite(ctx, path)
	require.NoError(t, err)
	saved := savedRestoreFixture(t, db.Checkpoints(), checkpoint.AwaitingApproval)
	action := workflow.ExecutableActions(saved.Workflow)[0]
	_, err = db.Approvals().Decide(ctx, approval.Decision{ID: approval.RequestID(action.ID), IntentID: saved.Workflow.ExecutionIntent.ID,
		ActionID: action.ID, ActionDigest: action.Digest, Status: approval.StatusApproved, DecidedBy: "human", DecisionReason: "reviewed"})
	require.NoError(t, err)
	require.NoError(t, db.Close())
	db, err = storage.OpenSQLite(ctx, path)
	require.NoError(t, err)
	defer db.Close()
	run, err := LoadAndReconcileWorkflow(ctx, db, saved.RunID, nil)
	require.NoError(t, err)
	assert.Equal(t, controller.ReconcileExecute, run.Reconciliation.Next)
	assert.Equal(t, workflow.StateExecuting, run.Reconciliation.Workflow.Snapshot().State)
	assert.Equal(t, saved, run.Checkpoint)
	after, err := db.Checkpoints().Get(ctx, saved.RunID)
	require.NoError(t, err)
	assert.Equal(t, saved, after)
	executions, err := db.Executions().ListIntent(ctx, saved.Workflow.ExecutionIntent.ID)
	require.NoError(t, err)
	assert.Empty(t, executions, "reconciliation must not prepare or execute actions")
}
