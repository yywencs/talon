package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/checkpoint"
	"github.com/wen/opentalon/internal/controller"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/storage"
	"github.com/wen/opentalon/internal/workflow"
)

func TestRunSavesThreeCheckpointBoundaries(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "talon.db")
	db, err := storage.OpenSQLite(ctx, path)
	require.NoError(t, err)
	defer db.Close()
	observer, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer observer.Close()
	_, err = observer.Exec(`CREATE TABLE observed_checkpoints (revision INTEGER, boundary TEXT)`)
	require.NoError(t, err)
	for _, event := range []string{"INSERT", "UPDATE"} {
		_, err = observer.Exec(fmt.Sprintf(`CREATE TRIGGER observe_%s AFTER %s ON run_checkpoints BEGIN
INSERT INTO observed_checkpoints VALUES (NEW.revision, json_extract(NEW.payload, '$.boundary')); END`, event, event))
		require.NoError(t, err)
	}
	result, err := Run(ctx, Config{
		DatasetRoot: testDatasetRoot(t), Storage: db, AutoApprove: false,
		ClockPollInterval: time.Millisecond, WorkerRetryInterval: time.Millisecond,
		InvestigatorFactory: func(flow *workflow.IncidentWorkflow, _ platform.ToolOpsPlatform) (controller.Investigator, error) {
			initial, err := db.Checkpoints().Get(ctx, flow.Snapshot().IntentIDPrefix)
			require.NoError(t, err, "initial checkpoint must precede investigator construction")
			assert.Equal(t, checkpoint.Created, initial.Boundary)
			assert.Equal(t, workflow.StateProtected, initial.Workflow.State)
			return &intentInvestigator{flow: flow, incidentID: flow.Snapshot().IncidentID}, nil
		},
	})
	require.NoError(t, err)
	rows, err := observer.Query(`SELECT revision, boundary FROM observed_checkpoints ORDER BY revision`)
	require.NoError(t, err)
	var boundaries []string
	var revisions []int
	for rows.Next() {
		var revision int
		var boundary string
		require.NoError(t, rows.Scan(&revision, &boundary))
		revisions = append(revisions, revision)
		boundaries = append(boundaries, boundary)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	assert.Equal(t, []int{1, 2, 3}, revisions)
	assert.Equal(t, []string{"created", "intent_accepted", "awaiting_approval"}, boundaries)
	saved, err := db.Checkpoints().Get(ctx, result.Artifact.RunID)
	require.NoError(t, err)
	assert.Equal(t, result.Artifact.RunID, saved.Workflow.IntentIDPrefix)
	wantJSON, err := json.Marshal(result.Controller.Snapshot)
	require.NoError(t, err)
	gotJSON, err := json.Marshal(saved.Workflow)
	require.NoError(t, err)
	assert.JSONEq(t, string(wantJSON), string(gotJSON))
	approvals, err := db.Approvals().ListPending(ctx)
	require.NoError(t, err)
	require.Len(t, approvals, 1)
	assert.Equal(t, saved.Workflow.ExecutionIntent.ID, approvals[0].IntentID)
}

func TestRunCheckpointFailureStopsProgress(t *testing.T) {
	for _, boundary := range []checkpoint.Boundary{checkpoint.Created, checkpoint.IntentAccepted, checkpoint.AwaitingApproval} {
		t.Run(string(boundary), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "talon.db")
			db, err := storage.OpenSQLite(ctx, path)
			require.NoError(t, err)
			defer db.Close()
			faults, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			defer faults.Close()
			for _, event := range []string{"INSERT", "UPDATE"} {
				_, err = faults.Exec(fmt.Sprintf(`CREATE TRIGGER fail_%s BEFORE %s ON run_checkpoints
WHEN json_extract(NEW.payload, '$.boundary') = '%s' BEGIN SELECT RAISE(ABORT, 'injected checkpoint failure'); END`, event, event, boundary))
				require.NoError(t, err)
			}
			constructed := false
			result, err := Run(ctx, Config{
				DatasetRoot: testDatasetRoot(t), Storage: db, AutoApprove: true,
				ClockPollInterval: time.Millisecond, WorkerRetryInterval: time.Millisecond,
				InvestigatorFactory: func(flow *workflow.IncidentWorkflow, _ platform.ToolOpsPlatform) (controller.Investigator, error) {
					constructed = true
					return &intentInvestigator{flow: flow, incidentID: flow.Snapshot().IncidentID}, nil
				},
			})
			require.ErrorContains(t, err, "injected checkpoint failure")
			assert.Equal(t, "failed", result.Artifact.Outcome)
			saved, loadErr := db.Checkpoints().Get(ctx, result.Artifact.RunID)
			if boundary == checkpoint.Created {
				assert.False(t, constructed)
				require.ErrorIs(t, loadErr, checkpoint.ErrNotFound)
			} else {
				require.NoError(t, loadErr)
				assert.True(t, result.World.Configs["mapping-v2"].Active, "no real rollback may occur after save failure")
				assert.Equal(t, 10, result.World.Routes["route-a"].Weight)
				if boundary == checkpoint.IntentAccepted {
					assert.Equal(t, checkpoint.Created, saved.Boundary)
					assert.Empty(t, result.World.Operations, "intent save must precede even DryRun")
				} else {
					assert.Equal(t, checkpoint.IntentAccepted, saved.Boundary)
					for _, operation := range result.World.Operations {
						assert.Equal(t, true, operation.Result["dry_run"])
					}
				}
			}
			approvals, err := db.Approvals().ListPending(ctx)
			require.NoError(t, err)
			assert.Empty(t, approvals, "approval transaction must roll back on checkpoint failure")
		})
	}
}

// A submitted intent remains durable even when the same investigation fails.
func TestRunSavesAcceptedIntentAfterInvestigationErrorOrCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		for _, failSave := range []bool{false, true} {
			t.Run(fmt.Sprintf("canceled=%t/saveFailure=%t", canceled, failSave), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				path := filepath.Join(t.TempDir(), "talon.db")
				db, err := storage.OpenSQLite(ctx, path)
				require.NoError(t, err)
				defer db.Close()
				if failSave {
					faults, err := sql.Open("sqlite", path)
					require.NoError(t, err)
					defer faults.Close()
					_, err = faults.Exec(`CREATE TRIGGER fail_intent BEFORE UPDATE ON run_checkpoints
WHEN json_extract(NEW.payload, '$.boundary') = 'intent_accepted' BEGIN SELECT RAISE(ABORT, 'injected intent failure'); END`)
					require.NoError(t, err)
				}
				investigationErr := errors.New("investigation failed after submission")
				if canceled {
					investigationErr = context.Canceled
				}
				result, err := Run(ctx, Config{
					DatasetRoot: testDatasetRoot(t), Storage: db, AutoApprove: true,
					InvestigatorFactory: func(flow *workflow.IncidentWorkflow, _ platform.ToolOpsPlatform) (controller.Investigator, error) {
						return &afterInvestigation{next: &intentInvestigator{flow: flow, incidentID: flow.Snapshot().IncidentID}, after: func() error {
							if canceled {
								cancel()
							}
							return investigationErr
						}}, nil
					},
				})
				require.ErrorIs(t, err, investigationErr)
				assert.Empty(t, result.World.Operations, "error must prevent DryRun")
				saved, loadErr := db.Checkpoints().Get(context.Background(), result.Artifact.RunID)
				require.NoError(t, loadErr)
				if failSave {
					require.ErrorContains(t, err, "injected intent failure")
					assert.Equal(t, checkpoint.Created, saved.Boundary)
				} else {
					assert.Equal(t, checkpoint.IntentAccepted, saved.Boundary)
					assert.Equal(t, result.Controller.Snapshot.ExecutionIntent.ID, saved.Workflow.ExecutionIntent.ID)
					assert.Equal(t, result.Artifact.RunConfig, saved.RunConfig)
					assert.Equal(t, result.Artifact.Provenance, saved.Provenance)
				}
				require.Len(t, result.Artifact.AgentRuns, 1)
				assert.Contains(t, result.Artifact.AgentRuns[0].Error, investigationErr.Error())
				persisted, loadErr := db.RunArtifacts().Get(context.Background(), result.Artifact.RunID)
				require.NoError(t, loadErr)
				assert.Equal(t, result.Artifact, persisted, "final audit must survive caller cancellation")
			})
		}
	}
}

type afterInvestigation struct {
	next  controller.Investigator
	after func() error
}

func (i *afterInvestigation) IncidentID() string { return i.next.IncidentID() }
func (i *afterInvestigation) Investigate(ctx context.Context, instruction string) error {
	if err := i.next.Investigate(ctx, instruction); err != nil {
		return err
	}
	return i.after()
}
