package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wen/opentalon/internal/execution"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/runlease"
)

// CommitObservedExecution records a verified terminal observation without
// submitting anything. It never steals a live action lease. The run lease and
// observed row identity/status are rechecked atomically with the write.
func (s *Storage) CommitObservedExecution(ctx context.Context, t runlease.Token, record execution.Record, op platform.Operation) error {
	if !strings.HasPrefix(record.IntentID, t.RunID+"-intent-") {
		return runlease.ErrLost
	}
	if record.Status != execution.StatusRunning && record.Status != execution.StatusUnknown {
		return execution.ErrConflict
	}
	status := execution.StatusFailed
	switch op.Status {
	case platform.OperationSucceeded:
		status = execution.StatusSucceeded
	case platform.OperationFailed, platform.OperationRejected:
	default:
		return fmt.Errorf("unconfirmed operation terminal status")
	}
	if op.ID != record.OperationID || op.IncidentID != record.IncidentID || op.IdempotencyKey != record.IdempotencyKey {
		return execution.ErrConflict
	}
	base := s.checkpoints.(*sqlCheckpointStore)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockRunLease(ctx, tx, base.driver, t); err != nil {
		return err
	}
	now := time.Now().UnixNano()
	result, err := tx.ExecContext(ctx, bindSQL(base.driver, `UPDATE action_executions SET status=?,operation_status=?,last_error=?,owner_id='',lease_until_unix_ns=NULL,
 next_poll_at_unix_ns=NULL,updated_at_unix_ns=?,finished_at_unix_ns=?
 WHERE action_id=? AND intent_id=? AND action_digest=? AND operation_id=? AND idempotency_key=? AND status=?
 AND (owner_id='' OR lease_until_unix_ns IS NULL OR lease_until_unix_ns<=?)`), status, string(op.Status), op.Message, now, now,
		record.ActionID, record.IntentID, record.ActionDigest, record.OperationID, record.IdempotencyKey, record.Status, now)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return execution.ErrNoClaimable
	}
	return tx.Commit()
}

// BindObservedOperation only fills a missing ID after read-only reconciliation.
// Run ownership and an expired/free action lease are checked in the same SQL transaction.
func (s *Storage) BindObservedOperation(ctx context.Context, t runlease.Token, record execution.Record, op platform.Operation) error {
	if !strings.HasPrefix(record.IntentID, t.RunID+"-intent-") {
		return runlease.ErrLost
	}
	if record.OperationID != "" || op.ID == "" || op.IncidentID != record.IncidentID || op.IdempotencyKey != record.IdempotencyKey || (record.Status != execution.StatusRunning && record.Status != execution.StatusUnknown) {
		return execution.ErrConflict
	}
	base := s.checkpoints.(*sqlCheckpointStore)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockRunLease(ctx, tx, base.driver, t); err != nil {
		return err
	}
	now := time.Now().UnixNano()
	result, err := tx.ExecContext(ctx, bindSQL(base.driver, `UPDATE action_executions SET operation_id=?,operation_status=?,updated_at_unix_ns=? WHERE action_id=? AND intent_id=? AND action_digest=? AND idempotency_key=? AND operation_id='' AND status=? AND (owner_id='' OR lease_until_unix_ns IS NULL OR lease_until_unix_ns<=?)`), op.ID, string(op.Status), now, record.ActionID, record.IntentID, record.ActionDigest, record.IdempotencyKey, record.Status, now)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return execution.ErrNoClaimable
	}
	return tx.Commit()
}
