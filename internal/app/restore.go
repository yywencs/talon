package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/wen/opentalon/internal/checkpoint"
	"github.com/wen/opentalon/internal/workflow"
)

// LoadedWorkflow is the saved boundary, not the latest external execution state.
// Keep its revision and run metadata for subsequent reconciliation and saving.
type LoadedWorkflow struct {
	Checkpoint checkpoint.Record
	Workflow   *workflow.IncidentWorkflow
}

// LoadWorkflow reads one run and rebuilds only its in-memory Workflow. It never
// creates a new run, writes an approval, starts a Controller or calls a platform.
// Approvals and execution records must be reconciled before resuming execution.
func LoadWorkflow(ctx context.Context, store checkpoint.Store, runID string) (*LoadedWorkflow, error) {
	if store == nil || strings.TrimSpace(runID) == "" {
		return nil, fmt.Errorf("checkpoint store and run ID are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runID = strings.TrimSpace(runID)
	record, err := store.Get(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("load run checkpoint: %w", err)
	}
	if err := record.Data.Validate(); err != nil {
		return nil, fmt.Errorf("validate run checkpoint: %w", err)
	}
	if record.RunID != runID || record.Workflow.IntentIDPrefix != runID || record.Revision == 0 {
		return nil, fmt.Errorf("checkpoint identity or revision does not match run")
	}
	flow, err := workflow.Restore(record.Workflow)
	if err != nil {
		return nil, err
	}
	return &LoadedWorkflow{Checkpoint: record, Workflow: flow}, nil
}
