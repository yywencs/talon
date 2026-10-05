package storage

import (
	"context"
	"database/sql"

	"github.com/wen/opentalon/internal/runlease"
)

// OwnedRun uses the same pool with fenced checkpoint, execution and audit writes.
// The returned view must not be closed independently from the parent Storage.
func (s *Storage) OwnedRun(t runlease.Token) *Storage {
	base := s.checkpoints.(*sqlCheckpointStore)
	d := base.driver
	return &Storage{db: s.db, approvals: s.approvals, checkpoints: s.OwnedCheckpoints(t),
		executions:   &sqlExecutionStore{db: s.db, driver: d, now: s.executions.(*sqlExecutionStore).now, lease: &t},
		runArtifacts: &sqlRunArtifactStore{db: s.db, driver: d, lease: &t}}
}
func guardedExec(ctx context.Context, db *sql.DB, driver Driver, token *runlease.Token, query string, args ...any) (sql.Result, error) {
	if token == nil {
		return db.ExecContext(ctx, query, args...)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := lockRunLease(ctx, tx, driver, *token); err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}
