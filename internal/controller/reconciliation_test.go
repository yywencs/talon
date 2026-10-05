package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/approval"
	"github.com/wen/opentalon/internal/execution"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/workflow"
)

type reconcileExecutionReader func(context.Context, string) ([]execution.Record, error)

func (f reconcileExecutionReader) ListIntent(ctx context.Context, id string) ([]execution.Record, error) {
	return f(ctx, id)
}

type reconcileApprovalReader func(context.Context, string) (approval.Request, error)

func (f reconcileApprovalReader) Get(ctx context.Context, id string) (approval.Request, error) {
	return f(ctx, id)
}

type reconcileOperationReader func(context.Context, platform.OperationQuery) (platform.Operation, error)

func (f reconcileOperationReader) GetOperation(ctx context.Context, q platform.OperationQuery) (platform.Operation, error) {
	return f(ctx, q)
}

func TestReconcileApprovalDecisionsAreReadOnly(t *testing.T) {
	for _, status := range []approval.Status{approval.StatusPending, approval.StatusApproved, approval.StatusRejected} {
		t.Run(string(status), func(t *testing.T) {
			ctx := context.Background()
			p, flow, _ := processorWithSuccessfulDryRun(t, platform.RemediationCapability{Name: "rollback_mapping", Risk: "medium", RequiresApproval: true})
			policies, err := p.EvaluatePolicy(ctx)
			require.NoError(t, err)
			snapshot := flow.Snapshot()
			id := approval.RequestID(policies[0].ActionID)
			if status != approval.StatusPending {
				_, err = p.approvalStore.Decide(ctx, approval.Decision{ID: id, IntentID: policies[0].IntentID, ActionID: policies[0].ActionID,
					ActionDigest: policies[0].ActionDigest, Status: status, DecidedBy: "oncall", DecisionReason: "reviewed"})
				require.NoError(t, err)
			}
			before, err := p.approvalStore.Get(ctx, id)
			require.NoError(t, err)
			cfg := ReconcileConfig{Approvals: p.approvalStore, Executions: reconcileExecutionReader(func(context.Context, string) ([]execution.Record, error) { return nil, nil })}
			for attempt := 0; attempt < 2; attempt++ {
				r, err := ReconcileWorkflow(ctx, snapshot, cfg)
				require.NoError(t, err)
				switch status {
				case approval.StatusPending:
					assert.Equal(t, ReconcileAwaitApproval, r.Next)
					assert.Equal(t, workflow.StateAwaitingApproval, r.Workflow.Snapshot().State)
				case approval.StatusApproved:
					assert.Equal(t, ReconcileExecute, r.Next)
					assert.Equal(t, workflow.StateExecuting, r.Workflow.Snapshot().State)
				case approval.StatusRejected:
					assert.Equal(t, ReconcileInvestigate, r.Next)
					assert.Equal(t, workflow.StateInvestigating, r.Workflow.Snapshot().State)
				}
				if status != approval.StatusPending {
					assert.Equal(t, *before.DecidedAt, r.Workflow.Snapshot().ActionApprovals[0].DecidedAt)
					assert.Equal(t, snapshot.Version+1, r.Workflow.Snapshot().Version)
				}
			}
			after, err := p.approvalStore.Get(ctx, id)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			assert.Equal(t, snapshot, flow.Snapshot())
		})
	}
}

func TestReconcileSuccessRecoversOutputsWithoutExecution(t *testing.T) {
	ctx := context.Background()
	service := executionPlatform("fix")
	service.executionResults["fix"] = map[string]any{"version": "v1", "nested": map[string]any{"count": 1}}
	p, flow, db := executingProcessor(t, service, "worker", time.Minute, []workflow.IntendedAction{{ToolName: "fix"}})
	defer db.Close()
	snapshot := flow.Snapshot()
	_, err := p.ExecuteNext(ctx)
	require.NoError(t, err)
	before, err := db.Executions().ListIntent(ctx, snapshot.ExecutionIntent.ID)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		r, err := ReconcileWorkflow(ctx, snapshot, ReconcileConfig{Approvals: db.Approvals(), Executions: db.Executions(), Operations: service})
		require.NoError(t, err)
		assert.Equal(t, ReconcileEvaluateStage, r.Next)
		assert.Equal(t, workflow.StateCheckpoint, r.Workflow.Snapshot().State)
		require.Len(t, r.Workflow.Snapshot().ActionResults, 1)
		assert.Equal(t, "v1", r.Workflow.Snapshot().ActionResults[0].Output["version"])
		assert.Equal(t, 1, r.Workflow.Snapshot().StagesExecuted)
		assert.Equal(t, 1, service.executeCalls)
	}
	after, err := db.Executions().ListIntent(ctx, snapshot.ExecutionIntent.ID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestReconcilePendingAndUnknownNeverResubmitOrStealLease(t *testing.T) {
	for _, lostResponse := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "unknown-without-operation-id"}[lostResponse], func(t *testing.T) {
			ctx := context.Background()
			service := executionPlatform("fix")
			service.async, service.stayPending, service.failFirstCall = true, true, lostResponse
			p, flow, db := executingProcessor(t, service, "other-worker", time.Minute, []workflow.IntendedAction{{ToolName: "fix"}})
			defer db.Close()
			snapshot := flow.Snapshot()
			_, err := p.ExecuteNext(ctx)
			if lostResponse {
				require.ErrorIs(t, err, ErrActionExecutionUnknown)
			} else {
				require.NoError(t, err)
			}
			before, err := db.Executions().ListIntent(ctx, snapshot.ExecutionIntent.ID)
			require.NoError(t, err)
			queries := 0
			cfg := ReconcileConfig{Approvals: db.Approvals(), Executions: db.Executions(), Operations: reconcileOperationReader(func(ctx context.Context, q platform.OperationQuery) (platform.Operation, error) {
				queries++
				return service.GetOperation(ctx, q)
			})}
			r, err := ReconcileWorkflow(ctx, snapshot, cfg)
			require.NoError(t, err)
			assert.Equal(t, ReconcileQueryOperation, r.Next)
			assert.Empty(t, r.Workflow.Snapshot().ActionResults)
			if lostResponse {
				assert.Zero(t, queries)
			} else {
				assert.Equal(t, 1, queries)
				service.stayPending = false
				r, err = ReconcileWorkflow(ctx, snapshot, cfg)
				require.NoError(t, err)
				assert.Equal(t, ReconcileCommitExecution, r.Next)
				assert.Equal(t, workflow.StateExecuting, r.Workflow.Snapshot().State)
				assert.Empty(t, r.Workflow.Snapshot().ActionResults)
			}
			after, err := db.Executions().ListIntent(ctx, snapshot.ExecutionIntent.ID)
			require.NoError(t, err)
			assert.Equal(t, before, after, "only a lease-owning worker may update executions")
			assert.Equal(t, 1, service.executeCalls)
		})
	}
}

func TestReconcileFailsClosedOnConflictingFacts(t *testing.T) {
	ctx := context.Background()
	service := executionPlatform("fix")
	p, flow, db := executingProcessor(t, service, "worker", time.Minute, []workflow.IntendedAction{{ToolName: "fix"}})
	defer db.Close()
	snapshot := flow.Snapshot()
	_, err := p.ExecuteNext(ctx)
	require.NoError(t, err)
	records, err := db.Executions().ListIntent(ctx, snapshot.ExecutionIntent.ID)
	require.NoError(t, err)
	for _, field := range []string{"digest", "key", "sequence", "operation-id", "operation-status"} {
		t.Run(field, func(t *testing.T) {
			cfg := ReconcileConfig{Approvals: db.Approvals(), Executions: db.Executions(), Operations: service}
			if field == "operation-id" || field == "operation-status" {
				cfg.Operations = reconcileOperationReader(func(ctx context.Context, q platform.OperationQuery) (platform.Operation, error) {
					op, err := service.GetOperation(ctx, q)
					if field == "operation-id" {
						op.ID = "wrong"
					} else {
						op.Status = platform.OperationFailed
					}
					return op, err
				})
			} else {
				cfg.Executions = reconcileExecutionReader(func(context.Context, string) ([]execution.Record, error) {
					copy := append([]execution.Record(nil), records...)
					switch field {
					case "digest":
						copy[0].ActionDigest = "wrong"
					case "key":
						copy[0].IdempotencyKey = "wrong"
					case "sequence":
						copy[0].Sequence++
					}
					return copy, nil
				})
			}
			r, err := ReconcileWorkflow(ctx, snapshot, cfg)
			require.ErrorIs(t, err, ErrReconcileConflict)
			assert.Nil(t, r)
		})
	}
	missing := ReconcileConfig{Approvals: db.Approvals(), Executions: db.Executions(), Operations: reconcileOperationReader(func(context.Context, platform.OperationQuery) (platform.Operation, error) {
		return platform.Operation{}, platform.ErrNotFound
	})}
	r, err := ReconcileWorkflow(ctx, snapshot, missing)
	require.NoError(t, err)
	assert.Equal(t, ReconcileQueryOperation, r.Next)
	assert.Empty(t, r.Workflow.Snapshot().ActionResults)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = ReconcileWorkflow(canceled, snapshot, missing)
	require.ErrorIs(t, err, context.Canceled)
}

func TestReconcileApprovalMissingOrForeignIsAnError(t *testing.T) {
	ctx := context.Background()
	p, flow, _ := processorWithSuccessfulDryRun(t, platform.RemediationCapability{Name: "rollback_mapping", Risk: "medium", RequiresApproval: true})
	_, err := p.EvaluatePolicy(ctx)
	require.NoError(t, err)
	snapshot := flow.Snapshot()
	for _, missing := range []bool{false, true} {
		cfg := ReconcileConfig{Approvals: reconcileApprovalReader(func(ctx context.Context, id string) (approval.Request, error) {
			if missing {
				return approval.Request{}, approval.ErrNotFound
			}
			value, err := p.approvalStore.Get(ctx, id)
			value.IntentID = "another-run-intent"
			return value, err
		}), Executions: reconcileExecutionReader(func(context.Context, string) ([]execution.Record, error) { return nil, nil })}
		r, err := ReconcileWorkflow(ctx, snapshot, cfg)
		require.Error(t, err)
		assert.True(t, errors.Is(err, approval.ErrNotFound) || errors.Is(err, ErrReconcileConflict))
		assert.Nil(t, r)
		assert.Equal(t, snapshot, flow.Snapshot())
	}
}

func TestReconcileFailedExecutionUsesWorkflowFailureRules(t *testing.T) {
	ctx := context.Background()
	service := executionPlatform("fix")
	service.executionStatus = platform.OperationFailed
	p, flow, db := executingProcessor(t, service, "worker", time.Minute, []workflow.IntendedAction{{ToolName: "fix"}})
	defer db.Close()
	snapshot := flow.Snapshot()
	_, err := p.ExecuteNext(ctx)
	require.Error(t, err)
	r, err := ReconcileWorkflow(ctx, snapshot, ReconcileConfig{Approvals: db.Approvals(), Executions: db.Executions()})
	require.NoError(t, err)
	assert.Equal(t, ReconcileInspectFailure, r.Next)
	assert.Equal(t, workflow.StateInvestigating, r.Workflow.Snapshot().State)
	assert.Equal(t, 1, r.Workflow.Snapshot().AgentResumesUsed)
	require.Len(t, r.Workflow.Snapshot().Failures, 1)
	assert.Equal(t, workflow.FailureCategoryExecutionFailed, r.Workflow.Snapshot().Failures[0].Category)
	assert.Equal(t, 1, service.executeCalls)
}

func TestReconcilePartialApprovalsAndLateReadFailure(t *testing.T) {
	ctx := context.Background()
	service := executionPlatform("first", "second")
	p, flow, db := executingProcessor(t, service, "worker", time.Minute,
		[]workflow.IntendedAction{{ToolName: "first"}, {ToolName: "second"}})
	defer db.Close()
	// Build another Workflow with approval-required capabilities using the same stores.
	flow, err := workflow.NewIncidentWorkflow(workflow.Config{IncidentID: "partial", IntentIDPrefix: "partial-run"})
	require.NoError(t, err)
	_, err = flow.Apply(workflow.Event{Type: workflow.EventStartInvestigation, Actor: workflow.ActorController})
	require.NoError(t, err)
	_, err = flow.SubmitExecutionIntent(workflow.ExecutionIntentDraft{Summary: "fix", RootCause: "test", EvidenceRefs: []string{"test"},
		Stages: []workflow.ExecutionStageDraft{{StageID: "fix", Goal: "fix", Actions: []workflow.IntendedAction{{ToolName: "first"}, {ToolName: "second"}}}}})
	require.NoError(t, err)
	for i := range service.capabilities {
		service.capabilities[i].Risk = "medium"
		service.capabilities[i].RequiresApproval = true
	}
	p, err = NewExecutionCoordinator(service, flow, WithApprovalStore(db.Approvals()))
	require.NoError(t, err)
	_, err = p.DryRun(ctx)
	require.NoError(t, err)
	policies, err := p.EvaluatePolicy(ctx)
	require.NoError(t, err)
	snapshot := flow.Snapshot()
	first := policies[0]
	_, err = db.Approvals().Decide(ctx, approval.Decision{ID: approval.RequestID(first.ActionID), IntentID: first.IntentID,
		ActionID: first.ActionID, ActionDigest: first.ActionDigest, Status: approval.StatusApproved, DecidedBy: "human"})
	require.NoError(t, err)
	cfg := ReconcileConfig{Approvals: db.Approvals(), Executions: db.Executions()}
	r, err := ReconcileWorkflow(ctx, snapshot, cfg)
	require.NoError(t, err)
	assert.Equal(t, ReconcileAwaitApproval, r.Next)
	require.Len(t, r.Workflow.Snapshot().ActionApprovals, 1)
	assert.Equal(t, workflow.StateAwaitingApproval, r.Workflow.Snapshot().State)
	// A later read failure must not leave the caller with a half-applied workflow.
	readErr := errors.New("approval storage unavailable")
	cfg.Approvals = reconcileApprovalReader(func(ctx context.Context, id string) (approval.Request, error) {
		if id == approval.RequestID(policies[1].ActionID) {
			return approval.Request{}, readErr
		}
		return db.Approvals().Get(ctx, id)
	})
	r, err = ReconcileWorkflow(ctx, snapshot, cfg)
	require.ErrorIs(t, err, readErr)
	assert.Nil(t, r)
	assert.Equal(t, snapshot, flow.Snapshot())
}

func TestReconcileStopsWhenCheckpointLacksLaterFacts(t *testing.T) {
	ctx := context.Background()
	service := executionPlatform("fix")
	_, flow, db := executingProcessor(t, service, "worker", time.Minute, []workflow.IntendedAction{{ToolName: "fix"}})
	defer db.Close()
	snapshot := flow.Snapshot()
	cfg := ReconcileConfig{Approvals: db.Approvals(), Executions: reconcileExecutionReader(func(context.Context, string) ([]execution.Record, error) {
		return []execution.Record{{Spec: execution.Spec{IncidentID: snapshot.IncidentID, IntentID: snapshot.ExecutionIntent.ID,
			ActionID: "later-stage-action"}, Status: execution.StatusSucceeded}}, nil
	})}
	r, err := ReconcileWorkflow(ctx, snapshot, cfg)
	require.NoError(t, err)
	assert.Equal(t, ReconcileMoreHistory, r.Next)
	assert.Equal(t, snapshot, r.Workflow.Snapshot())
	assert.Zero(t, service.executeCalls)
}
