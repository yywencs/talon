package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/wen/opentalon/internal/approval"
	"github.com/wen/opentalon/internal/checkpoint"
)

type sqlCheckpointStore struct {
	db     *sql.DB
	driver Driver
}

var _ checkpoint.Store = (*sqlCheckpointStore)(nil)

func (s *sqlCheckpointStore) Get(ctx context.Context, runID string) (checkpoint.Record, error) {
	return readCheckpoint(ctx, s.db, s.driver, strings.TrimSpace(runID))
}

func (s *sqlCheckpointStore) Save(ctx context.Context, data checkpoint.Data, expected uint64) (checkpoint.Record, error) {
	if data.Boundary == checkpoint.AwaitingApproval {
		return checkpoint.Record{}, fmt.Errorf("awaiting approval requires SaveWithApprovals")
	}
	return s.save(ctx, data, expected, nil)
}

func (s *sqlCheckpointStore) SaveWithApprovals(ctx context.Context, data checkpoint.Data, expected uint64, requests []approval.Request) (checkpoint.Record, error) {
	if data.Boundary != checkpoint.AwaitingApproval {
		return checkpoint.Record{}, fmt.Errorf("approval transaction requires awaiting_approval boundary")
	}
	if err := data.ValidateApprovals(requests); err != nil {
		return checkpoint.Record{}, err
	}
	return s.save(ctx, data, expected, requests)
}

func (s *sqlCheckpointStore) save(ctx context.Context, data checkpoint.Data, expected uint64, requests []approval.Request) (checkpoint.Record, error) {
	if err := data.Validate(); err != nil {
		return checkpoint.Record{}, err
	}
	if expected >= math.MaxInt64 {
		return checkpoint.Record{}, fmt.Errorf("checkpoint revision exceeds database range")
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return checkpoint.Record{}, fmt.Errorf("encode run checkpoint: %w", err)
	}
	// Do not redact or sanitize a checkpoint: silently changing frozen action
	// parameters would invalidate their digests. Unsupported payloads fail closed.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return checkpoint.Record{}, fmt.Errorf("begin checkpoint transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	approvalStore := &sqlApprovalStore{db: tx, driver: s.driver, now: time.Now}
	for _, request := range requests {
		if _, err := approvalStore.Create(ctx, request); err != nil {
			return checkpoint.Record{}, fmt.Errorf("save checkpoint approval: %w", err)
		}
	}
	placeholder := "?"
	if s.driver == DriverPostgres {
		placeholder = "CAST(? AS JSONB)"
	}
	now := time.Now().UTC()
	var result sql.Result
	if expected == 0 {
		query := `INSERT INTO run_checkpoints (run_id, schema_version, revision, payload, updated_at_unix_ns)
VALUES (?, ?, 1, ` + placeholder + `, ?) ON CONFLICT(run_id) DO NOTHING`
		result, err = tx.ExecContext(ctx, bindSQL(s.driver, query), data.RunID, data.SchemaVersion, string(payload), now.UnixNano())
	} else {
		query := `UPDATE run_checkpoints SET schema_version = ?, revision = revision + 1,
payload = ` + placeholder + `, updated_at_unix_ns = ? WHERE run_id = ? AND revision = ?`
		result, err = tx.ExecContext(ctx, bindSQL(s.driver, query), data.SchemaVersion, string(payload), now.UnixNano(), data.RunID, int64(expected))
	}
	if err != nil {
		return checkpoint.Record{}, fmt.Errorf("write run checkpoint: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return checkpoint.Record{}, fmt.Errorf("read checkpoint write result: %w", err)
	}
	persisted, err := readCheckpoint(ctx, tx, s.driver, data.RunID)
	if err != nil {
		if affected == 0 && errors.Is(err, checkpoint.ErrNotFound) {
			return checkpoint.Record{}, checkpoint.ErrConflict
		}
		return checkpoint.Record{}, err
	}
	if affected == 0 {
		previous, encodeErr := json.Marshal(persisted.Data)
		samePayload := encodeErr == nil && bytes.Equal(previous, payload)
		if s.driver == DriverPostgres && persisted.Revision == expected+1 {
			// JSONB normalizes numeric notation (for example 1e-7). Compare
			// using its own equality rather than the original JSON spelling.
			err := tx.QueryRowContext(ctx, bindSQL(s.driver, `SELECT payload = CAST(? AS JSONB) FROM run_checkpoints WHERE run_id = ?`), string(payload), data.RunID).Scan(&samePayload)
			if err != nil {
				return checkpoint.Record{}, fmt.Errorf("compare checkpoint retry: %w", err)
			}
		}
		if persisted.Revision != expected+1 || !samePayload {
			return checkpoint.Record{}, checkpoint.ErrConflict
		}
	}
	if err := tx.Commit(); err != nil {
		// The caller retains the same expected revision and payload. An identical
		// retry can recognize a commit whose acknowledgement was lost.
		return checkpoint.Record{}, fmt.Errorf("commit run checkpoint: %w", err)
	}
	return persisted, nil
}

func readCheckpoint(ctx context.Context, db approvalDB, driver Driver, runID string) (checkpoint.Record, error) {
	var result checkpoint.Record
	var payload []byte
	var schema string
	var updated int64
	err := db.QueryRowContext(ctx, bindSQL(driver, `SELECT schema_version, revision, payload,
updated_at_unix_ns FROM run_checkpoints WHERE run_id = ?`), runID).Scan(&schema, &result.Revision, &payload, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return checkpoint.Record{}, checkpoint.ErrNotFound
	}
	if err != nil {
		return checkpoint.Record{}, fmt.Errorf("read run checkpoint: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber() // Preserve integer action parameters beyond float64 precision.
	if err := decoder.Decode(&result.Data); err != nil {
		return checkpoint.Record{}, fmt.Errorf("decode run checkpoint: %w", err)
	}
	if result.RunID != runID || result.SchemaVersion != schema || result.Revision == 0 {
		return checkpoint.Record{}, fmt.Errorf("checkpoint metadata does not match payload")
	}
	if err := result.Data.Validate(); err != nil {
		return checkpoint.Record{}, err
	}
	result.UpdatedAt = time.Unix(0, updated).UTC()
	return result, nil
}
