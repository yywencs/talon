// Package checkpoint defines durable save boundaries for an incident run.
// A checkpoint is not yet a complete process-resume image: Agent conversation
// and platform operation state have separate lifecycles.
package checkpoint

import (
	"context"
	"errors"
	"time"

	"github.com/wen/opentalon/internal/approval"
	"github.com/wen/opentalon/internal/runmeta"
	"github.com/wen/opentalon/internal/workflow"
)

const SchemaVersion = "talon.run-checkpoint/v1"

type Boundary string

const (
	Created          Boundary = "created"
	IntentAccepted   Boundary = "intent_accepted"
	AwaitingApproval Boundary = "awaiting_approval"
)

var (
	ErrNotFound = errors.New("run checkpoint not found")
	ErrConflict = errors.New("run checkpoint revision conflicts with persisted data")
)

// Data contains the facts captured at a save boundary. Workflow includes action
// results, approvals, stage position and workflow budgets. ModelCallsUsed is
// observational accounting, not a durable reservation of in-flight LLM calls.
type Data struct {
	SchemaVersion  string             `json:"schema_version"`
	RunID          string             `json:"run_id"`
	Boundary       Boundary           `json:"boundary"`
	Provenance     runmeta.Provenance `json:"provenance"`
	RunConfig      runmeta.Config     `json:"run_config"`
	ModelCallsUsed int                `json:"model_calls_used"`
	Workflow       workflow.Snapshot  `json:"workflow"`
}

// Record separates storage revision from Workflow.Version, which does not
// advance for every change to a workflow's data.
type Record struct {
	Data
	Revision  uint64    `json:"revision"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Store interface {
	// Save creates at expectedRevision=0, otherwise uses compare-and-swap.
	// An identical retry of a committed write returns the existing revision.
	Save(ctx context.Context, data Data, expectedRevision uint64) (Record, error)
	// SaveWithApprovals commits all pending approval requests and the checkpoint
	// in one transaction. Save alone rejects awaiting-approval checkpoints.
	SaveWithApprovals(ctx context.Context, data Data, expectedRevision uint64, requests []approval.Request) (Record, error)
	Get(ctx context.Context, runID string) (Record, error)
}
