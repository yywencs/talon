package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/approval"
	"github.com/wen/opentalon/internal/checkpoint"
	"github.com/wen/opentalon/internal/runmeta"
	"github.com/wen/opentalon/internal/workflow"
)

func checkpointFixture(t *testing.T, runID string) (checkpoint.Data, *workflow.IncidentWorkflow) {
	t.Helper()
	flow, err := workflow.NewIncidentWorkflow(workflow.Config{IncidentID: "checkpoint-incident", IntentIDPrefix: runID})
	require.NoError(t, err)
	return checkpoint.Data{SchemaVersion: checkpoint.SchemaVersion, RunID: runID, Boundary: checkpoint.Created,
		Provenance: runmeta.Provenance{CodeVersion: "test", DatasetVersion: "test"},
		RunConfig:  runmeta.Config{MaxModelCalls: 64}, Workflow: flow.Snapshot()}, flow
}

func acceptCheckpointIntent(t *testing.T, data checkpoint.Data, flow *workflow.IncidentWorkflow, args ...map[string]any) checkpoint.Data {
	t.Helper()
	arguments := map[string]any{"target": "v1"}
	if len(args) > 0 {
		arguments = args[0]
	}
	_, err := flow.Apply(workflow.Event{Type: workflow.EventStartInvestigation, Actor: workflow.ActorController})
	require.NoError(t, err)
	_, err = flow.SubmitExecutionIntent(workflow.ExecutionIntentDraft{
		Summary: "repair", RootCause: "test failure", EvidenceRefs: []string{"evidence:test"},
		Stages: []workflow.ExecutionStageDraft{{StageID: "repair", Goal: "repair", Actions: []workflow.IntendedAction{{
			Key: "repair", ToolName: "repair", Arguments: arguments,
		}}, CheckpointPolicy: workflow.CheckpointPolicy{DefaultDecision: workflow.CheckpointSucceeded}}},
	})
	require.NoError(t, err)
	data.Boundary, data.Workflow = checkpoint.IntentAccepted, flow.Snapshot()
	data.ModelCallsUsed = 3
	return data
}

func approvalCheckpoint(t *testing.T, data checkpoint.Data, flow *workflow.IncidentWorkflow) (checkpoint.Data, []approval.Request) {
	t.Helper()
	data = acceptCheckpointIntent(t, data, flow)
	_, err := flow.ResolveCurrentStage()
	require.NoError(t, err)
	action := workflow.ExecutableActions(flow.Snapshot())[0]
	intentID := flow.Snapshot().ExecutionIntent.ID
	_, err = flow.RecordActionDryRun(workflow.ActionDryRun{IntentID: intentID, ActionID: action.ID,
		ActionDigest: action.Digest, OperationID: "dry-run", IdempotencyKey: action.ID + ":dry-run",
		Status: workflow.ActionDryRunSucceeded, OperationStatus: "succeeded"})
	require.NoError(t, err)
	_, err = flow.RecordActionPolicyDecisions([]workflow.ActionPolicyDecision{{IntentID: intentID,
		ActionID: action.ID, ActionDigest: action.Digest, DryRunOperationID: "dry-run", Risk: "medium",
		Outcome: workflow.ActionPolicyApprovalRequired, ReasonCode: "test", Reason: "approval needed"}})
	require.NoError(t, err)
	data.Boundary, data.Workflow = checkpoint.AwaitingApproval, flow.Snapshot()
	return data, []approval.Request{{ID: approval.RequestID(action.ID), IncidentID: data.Workflow.IncidentID,
		IntentID: intentID, ActionID: action.ID, ActionDigest: action.Digest, DryRunOperationID: "dry-run",
		ToolName: action.ToolName, Arguments: action.Arguments, Risk: "medium", PolicyReason: "approval needed"}}
}

func runCheckpointContract(t *testing.T, store checkpoint.Store, approvals approval.Store) {
	t.Helper()
	ctx := context.Background()
	runID := fmt.Sprintf("checkpoint-%d", time.Now().UnixNano())
	data, flow := checkpointFixture(t, runID)
	_, err := store.Get(ctx, runID)
	require.ErrorIs(t, err, checkpoint.ErrNotFound)
	created, err := store.Save(ctx, data, 0)
	require.NoError(t, err)
	assert.EqualValues(t, 1, created.Revision)
	retry, err := store.Save(ctx, data, 0)
	require.NoError(t, err)
	assert.Equal(t, created, retry, "lost commit acknowledgement must be safely retryable")

	// JSONB and JSON TEXT must both preserve integers larger than 2^53.
	accepted := acceptCheckpointIntent(t, data, flow, map[string]any{
		"target": "v1", "large_integer": int64(9007199254740993), "small_number": 1e-7,
	})
	saved, err := store.Save(ctx, accepted, 1)
	require.NoError(t, err)
	assert.EqualValues(t, 2, saved.Revision)
	assert.Equal(t, "9007199254740993", saved.Workflow.ExecutionIntent.Stages[0].Actions[0].Arguments["large_integer"].(json.Number).String())
	before, err := json.Marshal(accepted)
	require.NoError(t, err)
	after, err := json.Marshal(saved.Data)
	require.NoError(t, err)
	assert.JSONEq(t, string(before), string(after))
	retry, err = store.Save(ctx, accepted, 1)
	require.NoError(t, err)
	assert.Equal(t, saved, retry)
	_, err = store.Save(ctx, data, 1)
	require.ErrorIs(t, err, checkpoint.ErrConflict, "stale writer cannot replace newer checkpoint")
	invalid := data
	invalid.SchemaVersion = "future/v99"
	_, err = store.Save(ctx, invalid, 2)
	require.ErrorContains(t, err, "unsupported checkpoint schema")

	waitingData, waitingFlow := checkpointFixture(t, runID+"-approval")
	_, err = store.Save(ctx, waitingData, 0)
	require.NoError(t, err)
	waiting, requests := approvalCheckpoint(t, waitingData, waitingFlow)
	_, err = store.Save(ctx, waiting, 1)
	require.ErrorContains(t, err, "SaveWithApprovals")
	// Approval inserts precede the CAS write; a CAS conflict must roll them back.
	_, err = store.SaveWithApprovals(ctx, waiting, 0, requests)
	require.ErrorIs(t, err, checkpoint.ErrConflict)
	_, err = approvals.Get(ctx, requests[0].ID)
	require.ErrorIs(t, err, approval.ErrNotFound)
	waitingSaved, err := store.SaveWithApprovals(ctx, waiting, 1, requests)
	require.NoError(t, err)
	persistedApproval, err := approvals.Get(ctx, requests[0].ID)
	require.NoError(t, err)
	assert.Equal(t, approval.StatusPending, persistedApproval.Status)
	assert.Equal(t, requests[0].ActionDigest, persistedApproval.ActionDigest)
	retry, err = store.SaveWithApprovals(ctx, waiting, 1, requests)
	require.NoError(t, err)
	assert.Equal(t, waitingSaved, retry)

	requests[0].ActionDigest = "different-content"
	_, err = store.SaveWithApprovals(ctx, waiting, 2, requests)
	require.Error(t, err)
	unchanged, err := store.Get(ctx, waiting.RunID)
	require.NoError(t, err)
	assert.Equal(t, waitingSaved, unchanged)
}

func TestSQLiteCheckpointStoreContract(t *testing.T) {
	db, err := OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "checkpoint.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	runCheckpointContract(t, db.Checkpoints(), db.Approvals())
}

func TestPostgresCheckpointStoreContract(t *testing.T) {
	dsn := os.Getenv("TALON_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set TALON_TEST_POSTGRES_DSN to run PostgreSQL checkpoint tests")
	}
	db, err := Open(context.Background(), Config{Driver: DriverPostgres, DSN: dsn, AutoMigrate: true, MaxOpenConns: 4})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	runCheckpointContract(t, db.Checkpoints(), db.Approvals())
}

func TestCheckpointAndApprovalsSurviveDatabaseReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "checkpoint.db")
	db, err := OpenSQLite(ctx, path)
	require.NoError(t, err)
	data, flow := checkpointFixture(t, "reopen-run")
	_, err = db.Checkpoints().Save(ctx, data, 0)
	require.NoError(t, err)
	waiting, requests := approvalCheckpoint(t, data, flow)
	want, err := db.Checkpoints().SaveWithApprovals(ctx, waiting, 1, requests)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	db, err = OpenSQLite(ctx, path)
	require.NoError(t, err)
	defer db.Close()
	got, err := db.Checkpoints().Get(ctx, data.RunID)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, "reopen-run", got.Workflow.IntentIDPrefix)
	_, err = db.Approvals().Get(ctx, requests[0].ID)
	require.NoError(t, err)
}

func TestApprovalWriteFailureRollsBackCheckpoint(t *testing.T) {
	ctx := context.Background()
	db, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "checkpoint.db"))
	require.NoError(t, err)
	defer db.Close()
	data, flow := checkpointFixture(t, "approval-failure")
	original, err := db.Checkpoints().Save(ctx, data, 0)
	require.NoError(t, err)
	waiting, requests := approvalCheckpoint(t, data, flow)
	_, err = db.db.ExecContext(ctx, `CREATE TRIGGER fail_approval BEFORE INSERT ON approval_requests BEGIN SELECT RAISE(ABORT, 'injected approval failure'); END`)
	require.NoError(t, err)
	_, err = db.Checkpoints().SaveWithApprovals(ctx, waiting, 1, requests)
	require.ErrorContains(t, err, "injected approval failure")
	got, err := db.Checkpoints().Get(ctx, data.RunID)
	require.NoError(t, err)
	assert.Equal(t, original, got)
}

func TestCheckpointStorageAlwaysValidatesData(t *testing.T) {
	ctx := context.Background()
	db, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "validation.db"))
	require.NoError(t, err)
	defer db.Close()
	data, _ := checkpointFixture(t, "invalid-run")
	for _, mutate := range []func(*checkpoint.Data){
		func(d *checkpoint.Data) { d.SchemaVersion = "unsupported" },
		func(d *checkpoint.Data) { d.RunID = "" },
		func(d *checkpoint.Data) { d.Workflow.IncidentID = "" },
		func(d *checkpoint.Data) { d.Workflow.IntentIDPrefix = "" },
		func(d *checkpoint.Data) { d.ModelCallsUsed = -1 },
		func(d *checkpoint.Data) { d.Boundary = checkpoint.IntentAccepted },
		func(d *checkpoint.Data) { d.Workflow.State = workflow.StateInvestigating },
	} {
		invalid := data
		mutate(&invalid)
		_, err := db.Checkpoints().Save(ctx, invalid, 0)
		require.Error(t, err)
	}
	// The atomic entry point must validate the data as well as approval bindings.
	waitingData, flow := checkpointFixture(t, "invalid-waiting")
	waiting, requests := approvalCheckpoint(t, waitingData, flow)
	waiting.ModelCallsUsed = -1
	_, err = db.Checkpoints().SaveWithApprovals(ctx, waiting, 0, requests)
	require.Error(t, err)
	_, err = db.Approvals().Get(ctx, requests[0].ID)
	require.ErrorIs(t, err, approval.ErrNotFound)

	// Read validation must reject corrupted payloads even with matching row metadata.
	_, err = db.Checkpoints().Save(ctx, data, 0)
	require.NoError(t, err)
	data.ModelCallsUsed = -1
	payload, err := json.Marshal(data)
	require.NoError(t, err)
	_, err = db.db.ExecContext(ctx, `UPDATE run_checkpoints SET payload = ? WHERE run_id = ?`, string(payload), data.RunID)
	require.NoError(t, err)
	_, err = db.Checkpoints().Get(ctx, data.RunID)
	require.ErrorContains(t, err, "nonnegative usage")
}
