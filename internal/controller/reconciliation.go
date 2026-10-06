package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/wen/opentalon/internal/approval"
	"github.com/wen/opentalon/internal/execution"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/workflow"
)

// These read-only ports deliberately cannot approve, claim leases or submit actions.
type ApprovalReader interface {
	Get(context.Context, string) (approval.Request, error)
}
type ExecutionReader interface {
	ListIntent(context.Context, string) ([]execution.Record, error)
}
type OperationReader interface {
	GetOperation(context.Context, platform.OperationQuery) (platform.Operation, error)
}

type ReconcileConfig struct {
	Approvals  ApprovalReader
	Executions ExecutionReader
	Operations OperationReader
}

type ReconcileNext string

const (
	ReconcileInvestigate     ReconcileNext = "investigate"
	ReconcileValidate        ReconcileNext = "validate"
	ReconcileAwaitApproval   ReconcileNext = "await_approval"
	ReconcileExecute         ReconcileNext = "execute"
	ReconcileEvaluateStage   ReconcileNext = "evaluate_stage"
	ReconcileInspectFailure  ReconcileNext = "inspect_failure"
	ReconcileQueryOperation  ReconcileNext = "query_operation"
	ReconcileCommitExecution ReconcileNext = "commit_execution"
	ReconcileBindOperation   ReconcileNext = "bind_operation"
	ReconcileMoreHistory     ReconcileNext = "recover_more_history"
)

var ErrReconcileConflict = errors.New("reconciliation facts conflict with workflow")

// Reconciliation is an observation, not a lease or permission to execute.
// A worker must still enforce current approvals, leases and idempotency contracts.
type Reconciliation struct {
	Workflow   *workflow.IncidentWorkflow
	Next       ReconcileNext
	Reason     string
	Approvals  []approval.Request
	Executions []execution.Record
	Operations []platform.Operation
}

// ReconcileWorkflow builds a private copy, reads durable facts and supplements
// the current stage. It never mutates the input or writes to any external system.
// Unknown results, unavailable outputs and history beyond this stage stay blocked.
func ReconcileWorkflow(ctx context.Context, snapshot workflow.Snapshot, cfg ReconcileConfig) (*Reconciliation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.Approvals == nil || cfg.Executions == nil {
		return nil, fmt.Errorf("approval and execution readers are required")
	}
	flow, err := workflow.Restore(snapshot)
	if err != nil {
		return nil, err
	}
	r := &Reconciliation{Workflow: flow}
	if snapshot.ExecutionIntent == nil {
		if snapshot.State != workflow.StateProtected && snapshot.State != workflow.StateInvestigating {
			return nil, fmt.Errorf("reconciliation is not supported in state %q", snapshot.State)
		}
		r.Next = ReconcileInvestigate
		return r, nil
	}
	if snapshot.State != workflow.StateValidating && snapshot.State != workflow.StateAwaitingApproval && snapshot.State != workflow.StateExecuting {
		return nil, fmt.Errorf("reconciliation requires validating, awaiting_approval or executing state")
	}
	records, err := cfg.Executions.ListIntent(ctx, snapshot.ExecutionIntent.ID)
	if err != nil {
		return nil, fmt.Errorf("read execution facts: %w", err)
	}
	r.Executions = records
	// An accepted-intent checkpoint lacks later DryRun/policy facts. Do not
	// re-run validation over a potentially already executed intent.
	if snapshot.State == workflow.StateValidating {
		if len(records) != 0 {
			r.Next, r.Reason = ReconcileMoreHistory, "execution records exist after the validating checkpoint"
		} else {
			r.Next = ReconcileValidate
		}
		return r, nil
	}
	if err := reconcileApprovals(ctx, snapshot, cfg.Approvals, r); err != nil {
		return nil, err
	}

	expected := make(map[string]execution.Spec)
	for _, spec := range executionSpecs(snapshot) {
		expected[spec.ActionID] = spec
	}
	byID := make(map[string]execution.Record)
	for _, record := range records {
		if record.IncidentID != snapshot.IncidentID || record.IntentID != snapshot.ExecutionIntent.ID {
			return nil, fmt.Errorf("%w: execution belongs to another incident or intent", ErrReconcileConflict)
		}
		if _, exists := byID[record.ActionID]; exists {
			return nil, fmt.Errorf("%w: duplicate execution", ErrReconcileConflict)
		}
		byID[record.ActionID] = record
		if spec, current := expected[record.ActionID]; current {
			if spec != record.Spec {
				return nil, fmt.Errorf("%w: execution spec for %q", ErrReconcileConflict, record.ActionID)
			}
		} else {
			// Later stages require their own frozen resolved arguments and policies.
			// Do not claim that this partial snapshot is safe to resume.
			r.Next, r.Reason = ReconcileMoreHistory, "execution history extends beyond the current stage"
		}
	}
	if r.Next == ReconcileMoreHistory {
		return r, nil
	}
	if flow.Snapshot().State != workflow.StateExecuting {
		for _, record := range records {
			if record.Status != execution.StatusPending || record.Attempt != 0 || record.OperationID != "" {
				return nil, fmt.Errorf("%w: execution exists without current approval", ErrReconcileConflict)
			}
		}
		if flow.Snapshot().State == workflow.StateAwaitingApproval {
			r.Next = ReconcileAwaitApproval
		} else {
			r.Next = ReconcileInvestigate
		}
		return r, nil
	}
	// Validate all authorization facts before interpreting any platform result.
	if err := validateReconciledAuthorization(flow.Snapshot()); err != nil {
		return nil, err
	}
	return reconcileExecutions(ctx, snapshot, cfg, r, byID)
}

func reconcileApprovals(ctx context.Context, snapshot workflow.Snapshot, reader ApprovalReader, r *Reconciliation) error {
	for _, policy := range snapshot.ActionPolicies {
		if policy.Outcome != workflow.ActionPolicyApprovalRequired {
			continue
		}
		action := intendedAction(workflow.ExecutableActions(snapshot), policy.ActionID)
		if action == nil {
			return fmt.Errorf("%w: approval policy has no current action", ErrReconcileConflict)
		}
		request, err := reader.Get(ctx, approval.RequestID(action.ID))
		if err != nil {
			return fmt.Errorf("read approval %q: %w", action.ID, err)
		}
		if request.ID != approval.RequestID(action.ID) || request.IncidentID != snapshot.IncidentID ||
			request.IntentID != snapshot.ExecutionIntent.ID || request.ActionID != action.ID || request.ActionDigest != action.Digest ||
			request.ToolName != action.ToolName || request.DryRunOperationID != policy.DryRunOperationID || request.Risk != policy.Risk {
			return fmt.Errorf("%w: approval does not match frozen action %q", ErrReconcileConflict, action.ID)
		}
		if request.Status != approval.StatusPending && request.Status != approval.StatusApproved && request.Status != approval.StatusRejected {
			return fmt.Errorf("%w: unknown approval status", ErrReconcileConflict)
		}
		if request.Status != approval.StatusPending && (request.DecidedAt == nil || request.DecidedAt.IsZero() || request.DecidedBy == "") {
			return fmt.Errorf("%w: approval lacks decision identity or time", ErrReconcileConflict)
		}
		for _, saved := range snapshot.ActionApprovals {
			if saved.ActionID == action.ID && (string(saved.Decision) != string(request.Status) || saved.Approver != request.DecidedBy || saved.Reason != request.DecisionReason) {
				return fmt.Errorf("%w: stored decision disagrees with snapshot", ErrReconcileConflict)
			}
		}
		r.Approvals = append(r.Approvals, request)
	}
	// Read and validate the whole set first; approved decisions precede rejection
	// because rejection leaves awaiting_approval and returns to investigation.
	for _, status := range []approval.Status{approval.StatusApproved, approval.StatusRejected} {
		for _, request := range r.Approvals {
			if request.Status != status {
				continue
			}
			_, err := r.Workflow.RecordPersistedActionApproval(workflow.ActionApproval{
				IntentID: request.IntentID, ActionID: request.ActionID, ActionDigest: request.ActionDigest,
				Decision: workflow.ActionApprovalDecision(status), Approver: request.DecidedBy,
				Reason: request.DecisionReason, DecidedAt: *request.DecidedAt,
			})
			if err != nil {
				return fmt.Errorf("reconcile approval: %w", err)
			}
			if status == approval.StatusRejected {
				return nil
			}
		}
	}
	return nil
}

func validateReconciledAuthorization(s workflow.Snapshot) error {
	actions := workflow.ExecutableActions(s)
	if len(actions) == 0 || len(actions) != len(s.ActionPolicies) || len(actions) != len(s.ActionDryRuns) {
		return fmt.Errorf("%w: executing workflow lacks complete validation facts", ErrReconcileConflict)
	}
	for _, p := range s.ActionPolicies {
		if p.Outcome == workflow.ActionPolicyAutoApproved {
			continue
		}
		approved := false
		for _, a := range s.ActionApprovals {
			if a.ActionID == p.ActionID && a.Decision == workflow.ActionApprovalApproved {
				approved = true
			}
		}
		if p.Outcome != workflow.ActionPolicyApprovalRequired || !approved {
			return fmt.Errorf("%w: current action is not authorized", ErrReconcileConflict)
		}
	}
	return nil
}

func reconcileExecutions(ctx context.Context, snapshot workflow.Snapshot, cfg ReconcileConfig, r *Reconciliation, records map[string]execution.Record) (*Reconciliation, error) {
	actions := workflow.ExecutableActions(snapshot)
	predecessorSucceeded := true
	for _, action := range actions {
		record, exists := records[action.ID]
		if exists {
			switch record.Status {
			case execution.StatusPending, execution.StatusRunning, execution.StatusUnknown, execution.StatusSucceeded, execution.StatusFailed:
			default:
				return nil, fmt.Errorf("%w: unknown execution status", ErrReconcileConflict)
			}
			if !predecessorSucceeded && record.Status != execution.StatusPending {
				return nil, fmt.Errorf("%w: execution order is inconsistent", ErrReconcileConflict)
			}
		}
		for _, result := range snapshot.ActionResults {
			if result.ActionID == action.ID && (!exists || record.Status != execution.StatusSucceeded || record.OperationID != result.OperationID) {
				return nil, fmt.Errorf("%w: saved action output disagrees with execution record", ErrReconcileConflict)
			}
		}
		predecessorSucceeded = exists && record.Status == execution.StatusSucceeded
	}
	unfinished := false
	for _, action := range actions {
		record, exists := records[action.ID]
		if !exists || record.Status == execution.StatusPending {
			if exists && (record.Attempt != 0 || record.OperationID != "") {
				return nil, fmt.Errorf("%w: pending execution has submission facts", ErrReconcileConflict)
			}
			unfinished = true
			continue
		}
		if unfinished {
			return nil, fmt.Errorf("%w: later action ran before its predecessor succeeded", ErrReconcileConflict)
		}
		if record.Status == execution.StatusFailed {
			if record.OperationStatus != string(platform.OperationFailed) && record.OperationStatus != string(platform.OperationRejected) {
				r.Next, r.Reason = ReconcileQueryOperation, "cancelled, timed-out or unclassified failure still requires resource reconciliation"
				return r, nil
			}
			_, err := r.Workflow.FailCurrentStage(record.LastError, nil, actionExecutionFailure(record))
			if err != nil {
				return nil, err
			}
			r.Next = ReconcileInspectFailure
			return r, nil
		}
		if record.Status != execution.StatusSucceeded && record.Status != execution.StatusRunning && record.Status != execution.StatusUnknown {
			return nil, fmt.Errorf("%w: unknown execution status", ErrReconcileConflict)
		}
		if record.OperationID == "" || cfg.Operations == nil {
			r.Next, r.Reason = ReconcileQueryOperation, "original Operation ID or query adapter is unavailable; do not resubmit"
			return r, nil
		}
		queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		op, err := cfg.Operations.GetOperation(queryCtx, platform.OperationQuery{IncidentID: snapshot.IncidentID, OperationID: record.OperationID})
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			r.Next, r.Reason = ReconcileQueryOperation, fmt.Sprintf("query original operation %q: %v", record.OperationID, err)
			return r, nil
		}
		if err := validatePlatformOperation(op, snapshot.IncidentID, action, record.IdempotencyKey); err != nil || op.ID != record.OperationID {
			return nil, fmt.Errorf("%w: operation identity does not match execution", ErrReconcileConflict)
		}
		r.Operations = append(r.Operations, op)
		if record.Status != execution.StatusSucceeded {
			r.Next, r.Reason = ReconcileQueryOperation, "original operation remains nonterminal or unknown"
			if isTerminalOperation(op.Status) {
				r.Next, r.Reason = ReconcileCommitExecution, "terminal observation must be committed by the lease-owning worker before advancing"
			}
			return r, nil
		}
		if op.Status != platform.OperationSucceeded || record.OperationStatus != string(platform.OperationSucceeded) {
			return nil, fmt.Errorf("%w: persisted success disagrees with platform", ErrReconcileConflict)
		}
		for _, saved := range snapshot.ActionResults {
			if saved.ActionID != action.ID {
				continue
			}
			previous, encodeErr := json.Marshal(saved.Output)
			current, currentErr := json.Marshal(op.Result)
			if encodeErr != nil || currentErr != nil || !bytes.Equal(previous, current) {
				return nil, fmt.Errorf("%w: operation output differs from saved result", ErrReconcileConflict)
			}
		}
		_, err = r.Workflow.RecordActionResult(workflow.ActionResult{IntentID: record.IntentID,
			StageID: workflow.CurrentStage(snapshot).StageID, ActionID: action.ID, ActionDigest: action.Digest,
			OperationID: op.ID, OperationStatus: string(op.Status), Output: cloneMap(op.Result)})
		if err != nil {
			return nil, err
		}
	}
	if unfinished {
		r.Next = ReconcileExecute
		return r, nil
	}
	if _, err := r.Workflow.CompleteCurrentStage("reconciled all current stage actions from durable execution records"); err != nil {
		return nil, err
	}
	r.Next = ReconcileEvaluateStage
	return r, nil
}
