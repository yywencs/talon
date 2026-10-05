package app

import (
	"context"
	"fmt"

	"github.com/wen/opentalon/internal/checkpoint"
	"github.com/wen/opentalon/internal/controller"
	"github.com/wen/opentalon/internal/storage"
)

type ReconciledRun struct {
	Checkpoint     checkpoint.Record
	Reconciliation *controller.Reconciliation
}

// LoadAndReconcileWorkflow reads a saved boundary and newer facts. It does not
// save the reconstructed state, start workers or acquire permission to execute.
func LoadAndReconcileWorkflow(ctx context.Context, database *storage.Storage, runID string, operations controller.OperationReader) (*ReconciledRun, error) {
	if database == nil {
		return nil, fmt.Errorf("application storage is required")
	}
	loaded, err := LoadWorkflow(ctx, database.Checkpoints(), runID)
	if err != nil {
		return nil, err
	}
	r, err := controller.ReconcileWorkflow(ctx, loaded.Workflow.Snapshot(), controller.ReconcileConfig{
		Approvals: database.Approvals(), Executions: database.Executions(), Operations: operations,
	})
	if err != nil {
		return nil, err
	}
	return &ReconciledRun{Checkpoint: loaded.Checkpoint, Reconciliation: r}, nil
}
