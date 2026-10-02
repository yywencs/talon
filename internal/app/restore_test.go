package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/approval"
	"github.com/wen/opentalon/internal/checkpoint"
	"github.com/wen/opentalon/internal/storage"
	"github.com/wen/opentalon/internal/workflow"
)

func savedRestoreFixture(t *testing.T, store checkpoint.Store, boundary checkpoint.Boundary) checkpoint.Record {
	t.Helper()
	ctx := context.Background()
	flow, err := workflow.NewIncidentWorkflow(workflow.Config{IncidentID: "incident", IntentIDPrefix: "saved-run"})
	require.NoError(t, err)
	data := checkpoint.Data{SchemaVersion: checkpoint.SchemaVersion, RunID: "saved-run", Boundary: checkpoint.Created, Workflow: flow.Snapshot()}
	record, err := store.Save(ctx, data, 0)
	require.NoError(t, err)
	if boundary == checkpoint.Created {
		return record
	}
	_, err = flow.Apply(workflow.Event{Type: workflow.EventStartInvestigation, Actor: workflow.ActorController})
	require.NoError(t, err)
	arguments := map[string]any{"target": "test"}
	if boundary == checkpoint.IntentAccepted {
		arguments["large_integer"] = int64(9007199254740993)
	}
	_, err = flow.SubmitExecutionIntent(workflow.ExecutionIntentDraft{
		Summary: "verify", RootCause: "test", EvidenceRefs: []string{"evidence:test"},
		Stages: []workflow.ExecutionStageDraft{{StageID: "verify", Goal: "verify", Actions: []workflow.IntendedAction{{
			ToolName: "verify", Arguments: arguments,
		}}}},
	})
	require.NoError(t, err)
	data.Boundary, data.Workflow, data.ModelCallsUsed = checkpoint.IntentAccepted, flow.Snapshot(), 3
	record, err = store.Save(ctx, data, 1)
	require.NoError(t, err)
	if boundary == checkpoint.IntentAccepted {
		return record
	}
	_, err = flow.ResolveCurrentStage()
	require.NoError(t, err)
	action := workflow.ExecutableActions(flow.Snapshot())[0]
	intent := flow.Snapshot().ExecutionIntent.ID
	_, err = flow.RecordActionDryRun(workflow.ActionDryRun{IntentID: intent, ActionID: action.ID, ActionDigest: action.Digest,
		IdempotencyKey: "dry-run-key", OperationID: "dry-run", Status: workflow.ActionDryRunSucceeded, OperationStatus: "succeeded"})
	require.NoError(t, err)
	_, err = flow.RecordActionPolicyDecisions([]workflow.ActionPolicyDecision{{IntentID: intent, ActionID: action.ID,
		ActionDigest: action.Digest, DryRunOperationID: "dry-run", Risk: "medium", Outcome: workflow.ActionPolicyApprovalRequired,
		ReasonCode: "approval", Reason: "review"}})
	require.NoError(t, err)
	data.Boundary, data.Workflow = checkpoint.AwaitingApproval, flow.Snapshot()
	record, err = store.SaveWithApprovals(ctx, data, 2, []approval.Request{{ID: approval.RequestID(action.ID), IncidentID: "incident",
		IntentID: intent, ActionID: action.ID, ActionDigest: action.Digest, DryRunOperationID: "dry-run",
		ToolName: action.ToolName, Arguments: action.Arguments, Risk: "medium", PolicyReason: "review"}})
	require.NoError(t, err)
	return record
}

func TestLoadWorkflowFromReopenedDatabase(t *testing.T) {
	for _, boundary := range []checkpoint.Boundary{checkpoint.Created, checkpoint.IntentAccepted, checkpoint.AwaitingApproval} {
		t.Run(string(boundary), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "restore.db")
			db, err := storage.OpenSQLite(ctx, path)
			require.NoError(t, err)
			want := savedRestoreFixture(t, db.Checkpoints(), boundary)
			before, err := db.Approvals().ListPending(ctx)
			require.NoError(t, err)
			require.NoError(t, db.Close())
			db, err = storage.OpenSQLite(ctx, path)
			require.NoError(t, err)
			defer db.Close()
			loaded, err := LoadWorkflow(ctx, db.Checkpoints(), want.RunID)
			require.NoError(t, err)
			assert.Equal(t, want, loaded.Checkpoint)
			wantJSON, err := json.Marshal(want.Workflow)
			require.NoError(t, err)
			gotJSON, err := json.Marshal(loaded.Workflow.Snapshot())
			require.NoError(t, err)
			assert.Equal(t, string(wantJSON), string(gotJSON))
			if boundary == checkpoint.Created {
				_, err = loaded.Workflow.Apply(workflow.Event{Type: workflow.EventStartInvestigation, Actor: workflow.ActorController})
				require.NoError(t, err)
			} else {
				if boundary == checkpoint.IntentAccepted {
					value := loaded.Workflow.Snapshot().ExecutionIntent.Stages[0].Actions[0].Arguments["large_integer"]
					assert.Equal(t, json.Number("9007199254740993"), value)
					_, err = loaded.Workflow.ResolveCurrentStage()
					require.NoError(t, err)
				} else {
					_, err = loaded.Workflow.CompleteCurrentStage("must still wait")
					require.ErrorIs(t, err, workflow.ErrInvalidTransition)
				}
			}
			unchanged, err := db.Checkpoints().Get(ctx, want.RunID)
			require.NoError(t, err)
			assert.Equal(t, want, unchanged, "loading and in-memory operations must not overwrite checkpoints")
			after, err := db.Approvals().ListPending(ctx)
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}

func TestLoadWorkflowRejectsMissingAndInvalidData(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "restore.db"))
	require.NoError(t, err)
	defer db.Close()
	_, err = LoadWorkflow(ctx, db.Checkpoints(), "missing")
	require.ErrorIs(t, err, checkpoint.ErrNotFound)
	_, err = LoadWorkflow(ctx, nil, "run")
	require.Error(t, err)
	_, err = LoadWorkflow(ctx, db.Checkpoints(), " ")
	require.Error(t, err)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = LoadWorkflow(canceled, db.Checkpoints(), "run")
	require.ErrorIs(t, err, context.Canceled)
	record := savedRestoreFixture(t, db.Checkpoints(), checkpoint.IntentAccepted)
	record.Workflow.ActiveStageIndex = 99
	_, err = db.Checkpoints().Save(ctx, record.Data, record.Revision)
	require.NoError(t, err)
	loaded, err := LoadWorkflow(ctx, db.Checkpoints(), record.RunID)
	require.ErrorContains(t, err, "stage index")
	assert.Nil(t, loaded)
}

func TestLoadWorkflowDoesNotApplyNewerApprovalDecisions(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "restore.db"))
	require.NoError(t, err)
	defer db.Close()
	record := savedRestoreFixture(t, db.Checkpoints(), checkpoint.AwaitingApproval)
	action := workflow.ExecutableActions(record.Workflow)[0]
	_, err = db.Approvals().Decide(ctx, approval.Decision{ID: approval.RequestID(action.ID), IntentID: record.Workflow.ExecutionIntent.ID,
		ActionID: action.ID, ActionDigest: action.Digest, Status: approval.StatusApproved, DecidedBy: "oncall", DecisionReason: "reviewed"})
	require.NoError(t, err)
	loaded, err := LoadWorkflow(ctx, db.Checkpoints(), record.RunID)
	require.NoError(t, err)
	assert.Equal(t, workflow.StateAwaitingApproval, loaded.Workflow.Snapshot().State)
	assert.Empty(t, loaded.Workflow.Snapshot().ActionApprovals, "external approval reconciliation is a separate step")
	assert.Equal(t, record, loaded.Checkpoint)
}
