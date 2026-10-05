package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/execution"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/workflow"
)

func TestResumeReconciliationPreservesPartialResultsAndChecksOlderIntents(t *testing.T) {
	ctx := context.Background()
	service := executionPlatform("fix", "verify")
	p, flow, db := executingProcessor(t, service, "worker", time.Minute, []workflow.IntendedAction{{ToolName: "fix"}, {ToolName: "verify"}})
	defer db.Close()
	before := flow.Snapshot()
	oldIntent := before.ExecutionIntent.ID
	_, err := p.ExecuteNext(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, service.executeCalls)
	cfg := ReconcileConfig{Approvals: db.Approvals(), Executions: db.Executions(), Operations: service}
	r, err := ReconcileForResume(ctx, before, cfg)
	require.NoError(t, err)
	assert.Equal(t, ReconcileInvestigate, r.Next)
	require.Len(t, r.Workflow.Snapshot().ActionResults, 1)
	assert.Equal(t, 1, service.executeCalls, "pending old action must never be submitted")
	require.NoError(t, r.Workflow.ResumeInvestigation())
	_, err = r.Workflow.SubmitExecutionIntent(workflow.ExecutionIntentDraft{Summary: "new fault", RootCause: "changed cause", EvidenceRefs: []string{"new-evidence"}, Stages: []workflow.ExecutionStageDraft{{StageID: "new", Goal: "new goal", Actions: []workflow.IntendedAction{{ToolName: "different_fix"}}}}})
	require.NoError(t, err)
	newer := r.Workflow.Snapshot()
	require.NotEqual(t, oldIntent, newer.ExecutionIntent.ID)
	again, err := ReconcileForResume(ctx, newer, cfg)
	require.NoError(t, err)
	assert.Equal(t, ReconcileInvestigate, again.Next)
	assert.Len(t, again.Workflow.Snapshot().ActionResults, 1)
	_, err = workflow.Restore(again.Workflow.Snapshot())
	require.NoError(t, err)
	// Even a previous intent's uncertain operation blocks a new investigation.
	cfg.Executions = reconcileExecutionReader(func(ctx context.Context, id string) ([]execution.Record, error) {
		records, err := db.Executions().ListIntent(ctx, id)
		if id == oldIntent && len(records) > 0 {
			records[0].Status = execution.StatusUnknown
		}
		return records, err
	})
	newer.ActionResults = nil
	cfg.Operations = reconcileOperationReader(func(ctx context.Context, q platform.OperationQuery) (platform.Operation, error) {
		op, err := service.GetOperation(ctx, q)
		op.Status = platform.OperationRunning
		return op, err
	})
	blocked, err := ReconcileForResume(ctx, newer, cfg)
	require.NoError(t, err)
	assert.Equal(t, ReconcileQueryOperation, blocked.Next)
	assert.Equal(t, 1, service.executeCalls)
}
