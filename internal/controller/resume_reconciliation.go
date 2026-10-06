package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/wen/opentalon/internal/execution"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/workflow"
)

// ReconcileForResume examines every retained intent, including previous stages.
// It never submits an action or follows the old plan. Terminal observations whose
// execution rows lag are returned for an ownership-checked commit and reread.
func ReconcileForResume(ctx context.Context, s workflow.Snapshot, cfg ReconcileConfig) (*Reconciliation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.Approvals == nil || cfg.Executions == nil {
		return nil, fmt.Errorf("approval and execution readers required")
	}
	flow, err := workflow.Restore(s)
	if err != nil {
		return nil, err
	}
	r := &Reconciliation{Workflow: flow, Next: ReconcileInvestigate}
	if s.State == workflow.StateAwaitingApproval || s.State == workflow.StateExecuting {
		if err := reconcileApprovals(ctx, s, cfg.Approvals, r); err != nil {
			return nil, err
		}
	}
	resolved := map[string]workflow.ResolvedAction{}
	for _, a := range s.ResolvedActions {
		resolved[a.ActionID] = a
	}
	for _, intent := range s.ExecutionIntents {
		records, err := cfg.Executions.ListIntent(ctx, intent.ID)
		if err != nil {
			return nil, err
		}
		r.Executions = append(r.Executions, records...)
		seen := map[string]bool{}
		for _, record := range records {
			if seen[record.ActionID] {
				return nil, fmt.Errorf("%w: duplicate action", ErrReconcileConflict)
			}
			seen[record.ActionID] = true
			action, ok := resolved[record.ActionID]
			if !ok {
				r.Next = ReconcileMoreHistory
				r.Reason = "submitted history lacks frozen resolved action"
				return r, nil
			}
			sequence := 0
			found := false
			for _, stage := range intent.Stages {
				for _, a := range stage.Actions {
					sequence++
					if a.ID == record.ActionID {
						found = true
						break
					}
				}
				if found {
					break
				}
			}
			want := execution.Spec{IncidentID: s.IncidentID, IntentID: intent.ID, ActionID: action.ActionID, ActionDigest: action.Digest,
				Sequence: sequence, ToolName: action.ToolName, IdempotencyKey: action.ActionID + ":execute"}
			if !found || action.IntentID != intent.ID || record.Spec != want {
				return nil, fmt.Errorf("%w: execution spec", ErrReconcileConflict)
			}
			for _, saved := range s.ActionResults {
				if saved.ActionID == record.ActionID && (saved.OperationID != record.OperationID || (record.Status != execution.StatusSucceeded && record.Status != execution.StatusFailed)) {
					return nil, fmt.Errorf("%w: saved result and execution disagree", ErrReconcileConflict)
				}
			}
			if record.Status == execution.StatusPending {
				if record.Attempt != 0 || record.OperationID != "" || record.OwnerID != "" {
					return nil, fmt.Errorf("%w: pending action has submission facts", ErrReconcileConflict)
				}
				continue
			}
			authorized := false
			authSnapshot := flow.Snapshot()
			for _, policy := range authSnapshot.AllActionPolicies {
				if policy.ActionID != record.ActionID || policy.ActionDigest != record.ActionDigest {
					continue
				}
				if policy.Outcome == workflow.ActionPolicyAutoApproved {
					authorized = true
				}
				if policy.Outcome == workflow.ActionPolicyApprovalRequired {
					for _, decision := range authSnapshot.AllActionApprovals {
						if decision.ActionID == record.ActionID && decision.ActionDigest == record.ActionDigest && decision.Decision == workflow.ActionApprovalApproved {
							authorized = true
						}
					}
				}
			}
			if !authorized {
				return nil, fmt.Errorf("%w: submitted action lacks authorization facts", ErrReconcileConflict)
			}
			switch record.Status {
			case execution.StatusRunning, execution.StatusUnknown, execution.StatusSucceeded, execution.StatusFailed:
			default:
				return nil, fmt.Errorf("%w: execution status", ErrReconcileConflict)
			}
			if record.OperationID == "" {
				reader, ok := cfg.Operations.(platform.OperationKeyReader)
				if ok {
					queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
					op, lookupErr := reader.GetOperationByKey(queryCtx, platform.OperationKeyQuery{IncidentID: s.IncidentID, IdempotencyKey: record.IdempotencyKey})
					cancel()
					if lookupErr == nil {
						intended := workflow.IntendedAction{ID: action.ActionID, Digest: action.Digest, ToolName: action.ToolName, Kind: action.Kind, Arguments: action.Arguments}
						if op.ID == "" || validatePlatformOperation(op, s.IncidentID, intended, record.IdempotencyKey) != nil {
							return nil, fmt.Errorf("%w: recovered operation identity", ErrReconcileConflict)
						}
						r.Operations = append(r.Operations, op)
						r.Next = ReconcileBindOperation
						r.Reason = "persist recovered operation ID before terminal reconciliation"
						return r, nil
					}
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
				}
				r.Next = ReconcileQueryOperation
				r.Reason = "original operation not yet discoverable; request may remain in flight; do not resubmit"
				return r, nil
			}
			if cfg.Operations == nil {
				r.Next = ReconcileQueryOperation
				r.Reason = "operation reader unavailable"
				return r, nil
			}

			queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			op, err := cfg.Operations.GetOperation(queryCtx, platform.OperationQuery{IncidentID: s.IncidentID, OperationID: record.OperationID})
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				r.Next = ReconcileQueryOperation
				r.Reason = fmt.Sprintf("query original operation: %v", err)
				return r, nil
			}
			intended := workflow.IntendedAction{ID: action.ActionID, Digest: action.Digest, ToolName: action.ToolName, Kind: action.Kind, Arguments: action.Arguments}
			if err := validatePlatformOperation(op, s.IncidentID, intended, record.IdempotencyKey); err != nil || op.ID != record.OperationID {
				return nil, fmt.Errorf("%w: operation identity", ErrReconcileConflict)
			}
			r.Operations = append(r.Operations, op)
			if op.Status != platform.OperationSucceeded && op.Status != platform.OperationFailed && op.Status != platform.OperationRejected {
				r.Next = ReconcileQueryOperation
				r.Reason = "operation remains running, unknown, cancelled or timed out"
				return r, nil
			}
			if record.Status == execution.StatusRunning || record.Status == execution.StatusUnknown {
				r.Next = ReconcileCommitExecution
				r.Reason = "commit confirmed terminal result before investigation"
				return r, nil
			}
			if record.OperationStatus != string(op.Status) || (record.Status == execution.StatusSucceeded) != (op.Status == platform.OperationSucceeded) {
				return nil, fmt.Errorf("%w: persisted and platform terminal results differ", ErrReconcileConflict)
			}
			if err := flow.RecordRecoveredResult(workflow.ActionResult{IntentID: intent.ID, StageID: action.StageID, ActionID: action.ActionID,
				ActionDigest: action.Digest, OperationID: op.ID, OperationStatus: string(op.Status), Output: cloneMap(op.Result)}); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrReconcileConflict, err)
			}
		}
		// Every saved output must still have a corresponding durable execution row.
		for _, result := range s.ActionResults {
			if result.IntentID == intent.ID && !seen[result.ActionID] {
				return nil, fmt.Errorf("%w: saved output has no execution", ErrReconcileConflict)
			}
		}
	}

	// A fully completed current stage consumes its normal stage budget, but its
	// old checkpoint policy is never evaluated during process recovery.
	current := flow.Snapshot()
	if current.State == workflow.StateExecuting {
		actions := workflow.ExecutableActions(current)
		complete := len(actions) > 0
		for _, action := range actions {
			success := false
			for _, result := range current.ActionResults {
				if result.ActionID == action.ID && result.OperationStatus == string(platform.OperationSucceeded) {
					success = true
				}
			}
			complete = complete && success
		}
		if complete {
			if _, err := flow.CompleteCurrentStage("current stage completion confirmed during recovery"); err != nil {
				return nil, err
			}
		}
	}
	return r, nil
}
