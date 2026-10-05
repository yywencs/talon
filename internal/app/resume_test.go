package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/approval"
	"github.com/wen/opentalon/internal/checkpoint"
	"github.com/wen/opentalon/internal/controller"
	"github.com/wen/opentalon/internal/execution"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/runartifact"
	"github.com/wen/opentalon/internal/runlease"
	"github.com/wen/opentalon/internal/runmeta"
	"github.com/wen/opentalon/internal/simulator"
	"github.com/wen/opentalon/internal/storage"
	"github.com/wen/opentalon/internal/workflow"
)

type resumeConnector func(context.Context, platform.RunConnection) (platform.ToolOpsPlatform, error)

func (f resumeConnector) ConnectRun(ctx context.Context, c platform.RunConnection) (platform.ToolOpsPlatform, error) {
	return f(ctx, c)
}

type resumeInvestigator struct {
	flow *workflow.IncidentWorkflow
	call func(context.Context, string) error
}

func (i *resumeInvestigator) IncidentID() string { return i.flow.Snapshot().IncidentID }
func (i *resumeInvestigator) Investigate(ctx context.Context, instruction string) error {
	return i.call(ctx, instruction)
}

type resumePlatform struct {
	platform.ToolOpsPlatform
	operation platform.Operation
	queries   int
}

func (p *resumePlatform) GetOperation(context.Context, platform.OperationQuery) (platform.Operation, error) {
	p.queries++
	return p.operation, nil
}

func externalResumeFixture(t *testing.T) (*storage.Storage, checkpoint.Record) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "resume.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	saved := savedRestoreFixture(t, db.Checkpoints(), checkpoint.AwaitingApproval)
	saved.Provenance = runmeta.Provenance{CodeVersion: "test", DatasetVersion: "test"}
	saved.RunConfig = runmeta.Config{EnvironmentID: "original-environment", MaxModelCalls: 64, ContextVersion: runartifact.IncidentContextSchemaVersion}
	request, err := db.Approvals().Get(ctx, approval.RequestID(workflow.ExecutableActions(saved.Workflow)[0].ID))
	require.NoError(t, err)
	saved, err = db.Checkpoints().SaveWithApprovals(ctx, saved.Data, saved.Revision, []approval.Request{request})
	require.NoError(t, err)
	artifact := runartifact.New(saved.Workflow.IncidentID, saved.Provenance, saved.RunConfig).Snapshot()
	artifact.RunID = saved.RunID
	require.NoError(t, db.RunArtifacts().Upsert(ctx, artifact))
	return db, saved
}
func resumeConfigFor(t *testing.T, db *storage.Storage, saved checkpoint.Record, service platform.ToolOpsPlatform, call func(*workflow.IncidentWorkflow, string) error) ResumeConfig {
	return ResumeConfig{RunID: saved.RunID, ConnectedConfig: ConnectedConfig{Config: Config{Storage: db,
		InvestigatorFactory: func(flow *workflow.IncidentWorkflow, _ platform.ToolOpsPlatform) (controller.Investigator, error) {
			return &resumeInvestigator{flow: flow, call: func(_ context.Context, instruction string) error { return call(flow, instruction) }}, nil
		}}, Connector: resumeConnector(func(_ context.Context, c platform.RunConnection) (platform.ToolOpsPlatform, error) {
		assert.Equal(t, "original-environment", c.EnvironmentID)
		assert.Equal(t, saved.RunID, c.RunID)
		assert.Positive(t, c.Epoch)
		return service, nil
	})}}
}
func TestResumePendingApprovalInvestigatesWithoutExecutingOldPlan(t *testing.T) {
	db, saved := externalResumeFixture(t)
	calls := 0
	cfg := resumeConfigFor(t, db, saved, &resumePlatform{}, func(flow *workflow.IncidentWorkflow, instruction string) error {
		calls++
		assert.Contains(t, instruction, "必须读取当前环境的新证据")
		assert.Contains(t, instruction, saved.Workflow.ExecutionIntent.ID)
		assert.Equal(t, workflow.StateInvestigating, flow.Snapshot().State)
		durable, err := db.Checkpoints().Get(context.Background(), saved.RunID)
		require.NoError(t, err)
		assert.Equal(t, workflow.StateInvestigating, durable.Workflow.State, "reinvestigation boundary saved before model starts")
		_, err = flow.Apply(workflow.Event{Type: workflow.EventEscalated, Actor: workflow.ActorAgent, Reason: "new fault needs human review"})
		return err
	})
	result, err := Resume(context.Background(), cfg)
	require.NoError(t, err)
	assert.False(t, result.Waiting)
	assert.Equal(t, controller.StopEscalated, result.Controller.Reason)
	assert.Equal(t, 1, calls)
	records, err := db.Executions().ListIntent(context.Background(), saved.Workflow.ExecutionIntent.ID)
	require.NoError(t, err)
	assert.Empty(t, records)
	persisted, err := db.Checkpoints().Get(context.Background(), saved.RunID)
	require.NoError(t, err)
	assert.Equal(t, checkpoint.Progress, persisted.Boundary)
	assert.Equal(t, 3, persisted.ModelCallsUsed)
	_, err = LoadWorkflow(context.Background(), db.Checkpoints(), saved.RunID)
	require.NoError(t, err)
	_, err = Resume(context.Background(), cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "terminal task must not restart investigation")
}
func submittedResumeAction(t *testing.T, db *storage.Storage, saved checkpoint.Record, op platform.OperationStatus, keepLease bool) platform.Operation {
	t.Helper()
	ctx := context.Background()
	action := workflow.ExecutableActions(saved.Workflow)[0]
	_, err := db.Approvals().Decide(ctx, approval.Decision{ID: approval.RequestID(action.ID), IntentID: saved.Workflow.ExecutionIntent.ID, ActionID: action.ID, ActionDigest: action.Digest, Status: approval.StatusApproved, DecidedBy: "human", DecisionReason: "reviewed"})
	require.NoError(t, err)
	spec := execution.Spec{IncidentID: saved.Workflow.IncidentID, IntentID: saved.Workflow.ExecutionIntent.ID, ActionID: action.ID, ActionDigest: action.Digest, Sequence: 1, ToolName: action.ToolName, IdempotencyKey: action.ID + ":execute"}
	_, err = db.Executions().Prepare(ctx, []execution.Spec{spec})
	require.NoError(t, err)
	_, err = db.Executions().ClaimNext(ctx, execution.Claim{IntentID: spec.IntentID, OwnerID: "old-worker", LeaseDuration: time.Minute})
	require.NoError(t, err)
	if !keepLease {
		_, err = db.Executions().RecordOperation(ctx, action.ID, "old-worker", "original-operation", "running", execution.PollSchedule{NextPollAt: time.Now(), OperationDeadline: time.Now().Add(time.Hour)})
		require.NoError(t, err)
	}
	return platform.Operation{ID: "original-operation", IncidentID: saved.Workflow.IncidentID, Kind: platform.OperationRemediation, Name: action.ToolName, IdempotencyKey: spec.IdempotencyKey, Status: op, Result: map[string]any{"outcome": "original fault gone; new fault present"}}
}
func TestResumeReconcilesBeforeReinvestigation(t *testing.T) {
	for _, status := range []platform.OperationStatus{platform.OperationRunning, platform.OperationSucceeded, platform.OperationFailed, platform.OperationCancelled} {
		t.Run(string(status), func(t *testing.T) {
			db, saved := externalResumeFixture(t)
			op := submittedResumeAction(t, db, saved, status, false)
			service := &resumePlatform{operation: op}
			calls := 0
			cfg := resumeConfigFor(t, db, saved, service, func(flow *workflow.IncidentWorkflow, instruction string) error {
				calls++
				require.Len(t, flow.Snapshot().ActionResults, 1)
				assert.Equal(t, string(status), flow.Snapshot().ActionResults[0].OperationStatus)
				assert.Contains(t, instruction, "new fault present")
				_, err := flow.Apply(workflow.Event{Type: workflow.EventEscalated, Actor: workflow.ActorAgent, Reason: "new fault"})
				return err
			})
			result, err := Resume(context.Background(), cfg)
			require.NoError(t, err)
			terminal := status == platform.OperationSucceeded || status == platform.OperationFailed
			assert.Equal(t, !terminal, result.Waiting)
			if terminal {
				assert.Equal(t, 1, calls)
				assert.GreaterOrEqual(t, service.queries, 2)
			} else {
				assert.Zero(t, calls)
				after, err := db.Checkpoints().Get(context.Background(), saved.RunID)
				require.NoError(t, err)
				assert.Equal(t, saved, after)
			}
		})
	}
}
func TestResumeUnknownSubmissionDoesNotResubmitOrInvestigate(t *testing.T) {
	db, saved := externalResumeFixture(t)
	op := submittedResumeAction(t, db, saved, platform.OperationSucceeded, true)
	service := &resumePlatform{operation: op}
	cfg := resumeConfigFor(t, db, saved, service, func(*workflow.IncidentWorkflow, string) error {
		t.Fatal("must not investigate unknown submission")
		return nil
	})
	result, err := Resume(context.Background(), cfg)
	require.NoError(t, err)
	assert.True(t, result.Waiting)
	assert.Zero(t, service.queries)
}
func TestResumeCannotAcquireOccupiedRun(t *testing.T) {
	db, saved := externalResumeFixture(t)
	token, err := db.RunLeases().Acquire(context.Background(), saved.RunID, "first", time.Minute)
	require.NoError(t, err)
	defer db.RunLeases().Release(context.Background(), token)
	cfg := resumeConfigFor(t, db, saved, &resumePlatform{}, func(*workflow.IncidentWorkflow, string) error {
		t.Fatal("second agent must not investigate")
		return nil
	})
	_, err = Resume(context.Background(), cfg)
	require.ErrorIs(t, err, runlease.ErrBusy)
}
func TestResumeInterruptedInvestigationCanResumeAgain(t *testing.T) {
	db, saved := externalResumeFixture(t)
	failure := errors.New("model interrupted")
	calls := 0
	cfg := resumeConfigFor(t, db, saved, &resumePlatform{}, func(flow *workflow.IncidentWorkflow, _ string) error {
		calls++
		if calls == 1 {
			return failure
		}
		_, err := flow.Apply(workflow.Event{Type: workflow.EventEscalated, Actor: workflow.ActorAgent})
		return err
	})
	_, err := Resume(context.Background(), cfg)
	require.ErrorIs(t, err, failure)
	_, err = LoadWorkflow(context.Background(), db.Checkpoints(), saved.RunID)
	require.NoError(t, err)
	_, err = Resume(context.Background(), cfg)
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
}

// The Simulator object outlives both app invocations and database handles. This
// test connector stands in for the independently running server's fenced client.
func TestConnectedRunDatabaseReopenAndFreshApproval(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "connected.db")
	db, err := storage.OpenSQLite(ctx, path)
	require.NoError(t, err)
	prepared, err := prepareRun(Config{Storage: db, DatasetRoot: testDatasetRoot(t)})
	require.NoError(t, err)
	service, err := simulator.New(prepared.scenario)
	require.NoError(t, err)
	offset, err := agentStartOffset(prepared.scenario)
	require.NoError(t, err)
	require.NoError(t, service.Advance(ctx, offset))
	connections := 0
	investigations := 0
	cfg := ConnectedConfig{Config: Config{Storage: db, ScenarioID: prepared.scenario.Metadata.ID, Provenance: prepared.provenance,
		RunConfig: runmeta.Config{EnvironmentID: "same-external-instance"},
		InvestigatorFactory: func(flow *workflow.IncidentWorkflow, _ platform.ToolOpsPlatform) (controller.Investigator, error) {
			investigations++
			return &intentInvestigator{flow: flow, incidentID: flow.Snapshot().IncidentID}, nil
		}},
		Connector: resumeConnector(func(_ context.Context, c platform.RunConnection) (platform.ToolOpsPlatform, error) {
			connections++
			assert.Equal(t, "same-external-instance", c.EnvironmentID)
			return service, nil
		})}
	first, err := RunConnected(ctx, cfg)
	require.NoError(t, err)
	assert.Equal(t, controller.StopAwaitingApproval, first.Reason)
	original := first.Snapshot.ExecutionIntent.ID
	action := workflow.ExecutableActions(first.Snapshot)[0]
	_, err = db.Approvals().Decide(ctx, approval.Decision{ID: approval.RequestID(action.ID), IntentID: original, ActionID: action.ID, ActionDigest: action.Digest, Status: approval.StatusApproved, DecidedBy: "human", DecisionReason: "old plan"})
	require.NoError(t, err)
	require.NoError(t, db.Close())
	db, err = storage.OpenSQLite(ctx, path)
	require.NoError(t, err)
	defer db.Close()
	cfg.Storage = db
	resumed, err := Resume(ctx, ResumeConfig{ConnectedConfig: cfg, RunID: first.Snapshot.IntentIDPrefix})
	require.NoError(t, err)
	assert.Equal(t, controller.StopAwaitingApproval, resumed.Controller.Reason)
	assert.NotEqual(t, original, resumed.Controller.Snapshot.ExecutionIntent.ID)
	assert.Equal(t, first.Snapshot.IntentIDPrefix, resumed.Controller.Snapshot.IntentIDPrefix)
	assert.Equal(t, 2, connections)
	assert.Equal(t, 2, investigations)
	records, err := db.Executions().ListIntent(ctx, original)
	require.NoError(t, err)
	assert.Empty(t, records, "approved old plan must not execute on recovery")
	pending, err := db.Approvals().ListPending(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, resumed.Controller.Snapshot.ExecutionIntent.ID, pending[0].IntentID)
	artifact, err := db.RunArtifacts().Get(ctx, first.Snapshot.IntentIDPrefix)
	require.NoError(t, err)
	assert.Len(t, artifact.AgentRuns, 2)
}

func TestResumeTerminalObservationDoesNotStealLiveActionLease(t *testing.T) {
	db, saved := externalResumeFixture(t)
	ctx := context.Background()
	op := submittedResumeAction(t, db, saved, platform.OperationSucceeded, false)
	_, err := db.Executions().ClaimNext(ctx, execution.Claim{IntentID: saved.Workflow.ExecutionIntent.ID, OwnerID: "still-alive", LeaseDuration: time.Minute})
	require.NoError(t, err)
	cfg := resumeConfigFor(t, db, saved, &resumePlatform{operation: op}, func(*workflow.IncidentWorkflow, string) error { t.Fatal("must wait for live action lease"); return nil })
	result, err := Resume(ctx, cfg)
	require.NoError(t, err)
	assert.True(t, result.Waiting)
	records, err := db.Executions().ListIntent(ctx, saved.Workflow.ExecutionIntent.ID)
	require.NoError(t, err)
	assert.Equal(t, "still-alive", records[0].OwnerID)
	assert.Equal(t, execution.StatusRunning, records[0].Status)
}

func TestConnectedRunWaitsForHumanApprovalWithoutReinvestigating(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "approval-loop.db"))
	require.NoError(t, err)
	defer db.Close()
	prepared, err := prepareRun(Config{Storage: db, DatasetRoot: testDatasetRoot(t)})
	require.NoError(t, err)
	service, err := simulator.New(prepared.scenario)
	require.NoError(t, err)
	offset, err := agentStartOffset(prepared.scenario)
	require.NoError(t, err)
	require.NoError(t, service.Advance(ctx, offset))
	// A separate server owns time; a separate human approval client owns decisions.
	externalCtx, stopExternal := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-externalCtx.Done():
				done <- nil
				return
			case <-ticker.C:
				pending, err := db.Approvals().ListPending(externalCtx)
				if err != nil {
					if externalCtx.Err() != nil {
						done <- nil
					} else {
						done <- err
					}
					return
				}
				for _, request := range pending {
					_, err = db.Approvals().Decide(externalCtx, approval.Decision{ID: request.ID, IntentID: request.IntentID, ActionID: request.ActionID, ActionDigest: request.ActionDigest, Status: approval.StatusApproved, DecidedBy: "test-human", DecisionReason: "reviewed fresh plan"})
					if err != nil {
						if externalCtx.Err() != nil {
							done <- nil
						} else {
							done <- err
						}
						return
					}
				}
				world := service.Snapshot()
				if len(activeOperations(world.Operations)) > 0 {
					if err := service.Advance(externalCtx, world.Tick); err != nil {
						if externalCtx.Err() != nil {
							done <- nil
						} else {
							done <- err
						}
						return
					}
				}
			}
		}
	}()
	defer func() { stopExternal(); require.NoError(t, <-done) }()
	investigations := 0
	result, err := RunConnected(ctx, ConnectedConfig{Config: Config{Storage: db, ScenarioID: prepared.scenario.Metadata.ID, Provenance: prepared.provenance, WorkerRetryInterval: time.Millisecond,
		RunConfig: runmeta.Config{EnvironmentID: "server-instance"}, InvestigatorFactory: func(flow *workflow.IncidentWorkflow, _ platform.ToolOpsPlatform) (controller.Investigator, error) {
			investigations++
			return &intentInvestigator{flow: flow, incidentID: flow.Snapshot().IncidentID}, nil
		}},
		ApprovalPollInterval: time.Millisecond, Connector: resumeConnector(func(context.Context, platform.RunConnection) (platform.ToolOpsPlatform, error) { return service, nil })})
	require.NoError(t, err)
	assert.Equal(t, controller.StopResolved, result.Reason)
	assert.Equal(t, 1, investigations)
	loaded, err := LoadWorkflow(ctx, db.Checkpoints(), result.Snapshot.IntentIDPrefix)
	require.NoError(t, err)
	assert.Equal(t, workflow.StateResolved, loaded.Workflow.Snapshot().State)
}
