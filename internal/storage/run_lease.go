package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wen/opentalon/internal/runlease"
)

type sqlRunLeaseStore struct {
	db     *sql.DB
	driver Driver
}

func (s *Storage) RunLeases() runlease.Store {
	return &sqlRunLeaseStore{s.db, s.checkpoints.(*sqlCheckpointStore).driver}
}
func (s *sqlRunLeaseStore) Acquire(ctx context.Context, runID, owner string, ttl time.Duration) (runlease.Token, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(owner) == "" || ttl <= 0 {
		return runlease.Token{}, fmt.Errorf("run, owner and positive lease duration required")
	}
	now := leaseClock(s.driver)
	token := runlease.Token{RunID: runID, OwnerID: owner}
	err := s.db.QueryRowContext(ctx, bindSQL(s.driver, `INSERT INTO run_leases (run_id, owner_id, epoch, lease_until_unix_ns)
 VALUES (?, ?, 1, `+now+` + ?) ON CONFLICT(run_id) DO UPDATE SET owner_id=excluded.owner_id,
 epoch=run_leases.epoch+1, lease_until_unix_ns=excluded.lease_until_unix_ns
 WHERE run_leases.lease_until_unix_ns <= `+now+` RETURNING epoch`), runID, owner, int64(ttl)).Scan(&token.Epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return runlease.Token{}, runlease.ErrBusy
	}
	return token, err
}
func (s *sqlRunLeaseStore) Renew(ctx context.Context, t runlease.Token, ttl time.Duration) error {
	if ttl <= 0 {
		return fmt.Errorf("positive lease duration required")
	}
	result, err := s.db.ExecContext(ctx, bindSQL(s.driver, `UPDATE run_leases SET lease_until_unix_ns=`+leaseClock(s.driver)+` + ? WHERE run_id=? AND owner_id=? AND epoch=? AND lease_until_unix_ns>`+leaseClock(s.driver)), int64(ttl), t.RunID, t.OwnerID, t.Epoch)
	return leaseUpdate(result, err)
}
func (s *sqlRunLeaseStore) Release(ctx context.Context, t runlease.Token) error {
	result, err := s.db.ExecContext(ctx, bindSQL(s.driver, `UPDATE run_leases SET lease_until_unix_ns=0 WHERE run_id=? AND owner_id=? AND epoch=?`), t.RunID, t.OwnerID, t.Epoch)
	return leaseUpdate(result, err)
}
func leaseUpdate(r sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return runlease.ErrLost
	}
	return nil
}

// Lock the ownership row in the same transaction as the protected checkpoint.
// A takeover cannot interleave between the check and the checkpoint commit.
func lockRunLease(ctx context.Context, tx *sql.Tx, driver Driver, t runlease.Token) error {
	result, err := tx.ExecContext(ctx, bindSQL(driver, `UPDATE run_leases SET epoch=epoch WHERE run_id=? AND owner_id=? AND epoch=? AND lease_until_unix_ns>`+leaseClock(driver)), t.RunID, t.OwnerID, t.Epoch)
	return leaseUpdate(result, err)
}

// Database time avoids clock skew between competing Agent processes.
func leaseClock(driver Driver) string {
	if driver == DriverPostgres {
		return "CAST(EXTRACT(EPOCH FROM clock_timestamp()) * 1000000000 AS BIGINT)"
	}
	return "CAST((julianday('now') - 2440587.5) * 86400000000000 AS INTEGER)"
}
