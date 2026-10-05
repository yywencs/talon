package controller

import (
	"context"
	"fmt"

	"github.com/wen/opentalon/internal/workflow"
)

// RefreshApprovals observes human decisions for the current live workflow. It
// never approves a request itself. Process recovery must use reconciliation and
// reinvestigation instead; this method is only for an uninterrupted approval wait.
func (p *ExecutionCoordinator) RefreshApprovals(ctx context.Context) error {
	if p == nil || p.workflow == nil || p.approvalStore == nil {
		return fmt.Errorf("workflow and approval store required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	snapshot := p.workflow.Snapshot()
	if snapshot.State != workflow.StateAwaitingApproval {
		return fmt.Errorf("approval refresh requires awaiting approval")
	}
	if err := reconcileApprovals(ctx, snapshot, p.approvalStore, &Reconciliation{Workflow: p.workflow}); err != nil {
		return err
	}
	if p.workflow.Snapshot().Version == snapshot.Version {
		return nil
	}
	return p.persistWorkflowAudit(ctx)
}
