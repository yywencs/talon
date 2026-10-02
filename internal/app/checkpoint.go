package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/wen/opentalon/internal/approval"
	"github.com/wen/opentalon/internal/checkpoint"
	"github.com/wen/opentalon/internal/controller"
	"github.com/wen/opentalon/internal/runmeta"
	"github.com/wen/opentalon/internal/workflow"
)

// runCheckpointWriter owns the storage revision for one run. A failed save
// latches the error: this process must not proceed with uncommitted state.
type runCheckpointWriter struct {
	mu           sync.Mutex
	store        checkpoint.Store
	metadata     runmeta.Metadata
	usage        modelUsage
	revision     uint64
	lastIntentID string
	failure      error
}

func (w *runCheckpointWriter) saveIntent(ctx context.Context, snapshot workflow.Snapshot) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failure != nil {
		return w.failure
	}
	if snapshot.State != workflow.StateValidating || snapshot.ExecutionIntent == nil || snapshot.ExecutionIntent.ID == w.lastIntentID {
		return nil
	}
	if err := w.saveLocked(ctx, checkpoint.IntentAccepted, snapshot, nil); err != nil {
		return err
	}
	w.lastIntentID = snapshot.ExecutionIntent.ID
	return nil
}

func (w *runCheckpointWriter) saveAwaitingApproval(ctx context.Context, snapshot workflow.Snapshot, approvals []approval.Request) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.saveLocked(ctx, checkpoint.AwaitingApproval, snapshot, approvals)
}

func (w *runCheckpointWriter) saveLocked(ctx context.Context, boundary checkpoint.Boundary, snapshot workflow.Snapshot, approvals []approval.Request) error {
	if w.failure != nil {
		return w.failure
	}
	data := checkpoint.Data{
		SchemaVersion: checkpoint.SchemaVersion, RunID: w.metadata.RunID, Boundary: boundary,
		Provenance: w.metadata.Provenance, RunConfig: w.metadata.Config,
		ModelCallsUsed: w.usage.ModelCallsUsed(), Workflow: snapshot,
	}
	// Preserve accepted intent facts even when the investigation was cancelled.
	// This bounded write is not a guarantee against process termination.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var record checkpoint.Record
	var err error
	if boundary == checkpoint.AwaitingApproval {
		record, err = w.store.SaveWithApprovals(persistCtx, data, w.revision, approvals)
	} else {
		record, err = w.store.Save(persistCtx, data, w.revision)
	}
	if err != nil {
		w.failure = fmt.Errorf("persist %s run checkpoint: %w", boundary, err)
		return w.failure
	}
	w.revision = record.Revision
	return nil
}

// modelUsage exposes accounting without coupling checkpoint writes to audit history.
type modelUsage interface{ ModelCallsUsed() int }

func (w *runCheckpointWriter) saveCreated(ctx context.Context, snapshot workflow.Snapshot) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.saveLocked(ctx, checkpoint.Created, snapshot, nil)
}

// intentPersistenceGate must finish before the controller may enter DryRun.
// An accepted intent is saved even when investigation returns an error or cancellation.
// It is independent of audit decoration and never starts background persistence.
type intentPersistenceGate struct {
	next        controller.Investigator
	workflow    *workflow.IncidentWorkflow
	checkpoints *runCheckpointWriter
}

func (g *intentPersistenceGate) IncidentID() string { return g.next.IncidentID() }
func (g *intentPersistenceGate) Investigate(ctx context.Context, instruction string) error {
	err := g.next.Investigate(ctx, instruction)
	return errors.Join(err, g.checkpoints.saveIntent(ctx, g.workflow.Snapshot()))
}

var _ controller.Investigator = (*intentPersistenceGate)(nil)
