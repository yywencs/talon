package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wen/opentalon/internal/execution"
)

type sqlExecutionStore struct {
	db     *sql.DB
	driver Driver
	now    func() time.Time
}

var _ execution.Store = (*sqlExecutionStore)(nil)

func newSQLExecutionStore(db *sql.DB, driver Driver) *sqlExecutionStore {
	return &sqlExecutionStore{db: db, driver: driver, now: time.Now}
}

// Prepare 原子创建一个冻结 ExecutionIntent 的 Action 执行记录；相同规格可幂等重试，内容不一致则返回冲突。
func (s *sqlExecutionStore) Prepare(ctx context.Context, specs []execution.Spec) ([]execution.Record, error) {
	if len(specs) == 0 {
		return nil, fmt.Errorf("action execution specs are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin action execution preparation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now().UTC().UnixNano()
	seen := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		if err := validateExecutionSpec(spec); err != nil {
			return nil, err
		}
		if _, exists := seen[spec.ActionID]; exists {
			return nil, fmt.Errorf("duplicate action execution %q", spec.ActionID)
		}
		seen[spec.ActionID] = struct{}{}
		_, err := tx.ExecContext(ctx, bindSQL(s.driver, `INSERT INTO action_executions (
    action_id, incident_id, intent_id, action_digest, sequence_no, tool_name,
    idempotency_key, status, created_at_unix_ns, updated_at_unix_ns
) VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)
ON CONFLICT DO NOTHING`), spec.ActionID, spec.IncidentID, spec.IntentID, spec.ActionDigest,
			spec.Sequence, spec.ToolName, spec.IdempotencyKey, now, now)
		if err != nil {
			return nil, fmt.Errorf("prepare action execution %q: %w", spec.ActionID, err)
		}
		persisted, err := getExecution(ctx, tx, s.driver, spec.ActionID)
		if err != nil {
			return nil, err
		}
		if persisted.Spec != spec {
			return nil, fmt.Errorf("%w: action %q is already bound to different immutable content", execution.ErrConflict, spec.ActionID)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit action execution preparation: %w", err)
	}
	records, err := s.ListIntent(ctx, specs[0].IntentID)
	if err != nil {
		return nil, err
	}
	requested := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		requested[spec.ActionID] = struct{}{}
	}
	selected := make([]execution.Record, 0, len(specs))
	for _, record := range records {
		if _, ok := requested[record.ActionID]; ok {
			selected = append(selected, record)
		}
	}
	if len(selected) != len(specs) {
		return nil, fmt.Errorf("%w: intent %q has unexpected action execution records", execution.ErrConflict, specs[0].IntentID)
	}
	return selected, nil
}

func (s *sqlExecutionStore) ClaimNext(ctx context.Context, claim execution.Claim) (execution.Record, error) {
	claim.IntentID = strings.TrimSpace(claim.IntentID)
	claim.OwnerID = strings.TrimSpace(claim.OwnerID)
	if claim.IntentID == "" || claim.OwnerID == "" || claim.LeaseDuration <= 0 {
		return execution.Record{}, fmt.Errorf("intent ID, owner ID and positive lease duration are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.Record{}, fmt.Errorf("begin action execution claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	query := selectExecution + ` WHERE current.intent_id = ?
AND current.status <> 'succeeded'
AND (current.next_poll_at_unix_ns IS NULL OR current.next_poll_at_unix_ns <= ?)
AND NOT EXISTS (
    SELECT 1 FROM action_executions previous
    WHERE previous.intent_id = current.intent_id
      AND previous.sequence_no < current.sequence_no
      AND previous.status <> 'succeeded'
)
ORDER BY current.sequence_no
LIMIT 1`
	if s.driver == DriverPostgres {
		query += ` FOR UPDATE`
	}
	now := s.now().UTC()
	record, err := scanExecution(tx.QueryRowContext(ctx, bindSQL(s.driver, query), claim.IntentID, now.UnixNano()))
	if errors.Is(err, execution.ErrNotFound) {
		return execution.Record{}, execution.ErrNoClaimable
	}
	if err != nil {
		return execution.Record{}, err
	}
	if record.Status == execution.StatusFailed {
		return execution.Record{}, execution.ErrNoClaimable
	}
	ownedAndLive := (record.Status == execution.StatusRunning || record.Status == execution.StatusUnknown) &&
		record.OwnerID == claim.OwnerID && record.LeaseUntil != nil && record.LeaseUntil.After(now)
	expired := (record.Status == execution.StatusRunning || record.Status == execution.StatusUnknown) &&
		(record.LeaseUntil == nil || !record.LeaseUntil.After(now))
	if record.Status != execution.StatusPending && !ownedAndLive && !expired {
		return execution.Record{}, execution.ErrNoClaimable
	}
	attemptIncrement := 1
	if ownedAndLive {
		attemptIncrement = 0
	}
	leaseUntil := now.Add(claim.LeaseDuration)
	result, err := tx.ExecContext(ctx, bindSQL(s.driver, `UPDATE action_executions
SET status = 'running', owner_id = ?, lease_until_unix_ns = ?,
    next_poll_at_unix_ns = NULL, attempt = attempt + ?, updated_at_unix_ns = ?
WHERE action_id = ? AND status = ? AND owner_id = ?`),
		claim.OwnerID, leaseUntil.UnixNano(), attemptIncrement, now.UnixNano(),
		record.ActionID, record.Status, record.OwnerID)
	if err != nil {
		return execution.Record{}, fmt.Errorf("claim action execution %q: %w", record.ActionID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return execution.Record{}, execution.ErrNoClaimable
	}
	claimed, err := getExecution(ctx, tx, s.driver, record.ActionID)
	if err != nil {
		return execution.Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return execution.Record{}, fmt.Errorf("commit action execution claim: %w", err)
	}
	return claimed, nil
}

func (s *sqlExecutionStore) Renew(ctx context.Context, actionID, ownerID string, leaseDuration time.Duration) (execution.Record, error) {
	if leaseDuration <= 0 {
		return execution.Record{}, fmt.Errorf("positive lease duration is required")
	}
	now := s.now().UTC()
	result, err := s.db.ExecContext(ctx, bindSQL(s.driver, `UPDATE action_executions
SET lease_until_unix_ns = ?, updated_at_unix_ns = ?
WHERE action_id = ? AND owner_id = ? AND status IN ('running', 'unknown')`),
		now.Add(leaseDuration).UnixNano(), now.UnixNano(), strings.TrimSpace(actionID), strings.TrimSpace(ownerID))
	if err != nil {
		return execution.Record{}, fmt.Errorf("renew action execution lease: %w", err)
	}
	return s.requireOwnedUpdate(ctx, actionID, result)
}

func (s *sqlExecutionStore) RecordOperation(ctx context.Context, actionID, ownerID, operationID, operationStatus string, schedule execution.PollSchedule) (execution.Record, error) {
	if err := validatePollSchedule(schedule); err != nil {
		return execution.Record{}, err
	}
	now := s.now().UTC().UnixNano()
	result, err := s.db.ExecContext(ctx, bindSQL(s.driver, `UPDATE action_executions
SET status = 'running', operation_id = ?, operation_status = ?, last_error = '',
    owner_id = '', lease_until_unix_ns = NULL, next_poll_at_unix_ns = ?,
    operation_deadline_unix_ns = COALESCE(operation_deadline_unix_ns, ?), updated_at_unix_ns = ?
WHERE action_id = ? AND owner_id = ? AND status IN ('running', 'unknown')`),
		strings.TrimSpace(operationID), strings.TrimSpace(operationStatus), schedule.NextPollAt.UTC().UnixNano(),
		schedule.OperationDeadline.UTC().UnixNano(), now,
		strings.TrimSpace(actionID), strings.TrimSpace(ownerID))
	if err != nil {
		return execution.Record{}, fmt.Errorf("record platform operation: %w", err)
	}
	return s.requireOwnedUpdate(ctx, actionID, result)
}

func (s *sqlExecutionStore) MarkUnknown(ctx context.Context, actionID, ownerID, operationID, operationStatus, message string, schedule execution.PollSchedule) (execution.Record, error) {
	if err := validatePollSchedule(schedule); err != nil {
		return execution.Record{}, err
	}
	now := s.now().UTC().UnixNano()
	result, err := s.db.ExecContext(ctx, bindSQL(s.driver, `UPDATE action_executions
SET status = 'unknown', operation_id = ?, operation_status = ?, last_error = ?,
    owner_id = '', lease_until_unix_ns = NULL, next_poll_at_unix_ns = ?,
    operation_deadline_unix_ns = COALESCE(operation_deadline_unix_ns, ?), updated_at_unix_ns = ?
WHERE action_id = ? AND owner_id = ? AND status IN ('running', 'unknown')`),
		strings.TrimSpace(operationID), strings.TrimSpace(operationStatus), strings.TrimSpace(message),
		schedule.NextPollAt.UTC().UnixNano(), schedule.OperationDeadline.UTC().UnixNano(), now,
		strings.TrimSpace(actionID), strings.TrimSpace(ownerID))
	if err != nil {
		return execution.Record{}, fmt.Errorf("mark action execution unknown: %w", err)
	}
	return s.requireOwnedUpdate(ctx, actionID, result)
}

func (s *sqlExecutionStore) Complete(ctx context.Context, actionID, ownerID, operationID, operationStatus string, status execution.Status, message string) (execution.Record, error) {
	if status != execution.StatusSucceeded && status != execution.StatusFailed {
		return execution.Record{}, fmt.Errorf("action completion status must be succeeded or failed")
	}
	now := s.now().UTC().UnixNano()
	result, err := s.db.ExecContext(ctx, bindSQL(s.driver, `UPDATE action_executions
SET status = ?, operation_id = ?, operation_status = ?, last_error = ?,
    owner_id = '', lease_until_unix_ns = NULL, next_poll_at_unix_ns = NULL,
    updated_at_unix_ns = ?, finished_at_unix_ns = ?
WHERE action_id = ? AND owner_id = ? AND status IN ('running', 'unknown')`),
		status, strings.TrimSpace(operationID), strings.TrimSpace(operationStatus), strings.TrimSpace(message),
		now, now, strings.TrimSpace(actionID), strings.TrimSpace(ownerID))
	if err != nil {
		return execution.Record{}, fmt.Errorf("complete action execution: %w", err)
	}
	return s.requireOwnedUpdate(ctx, actionID, result)
}

func (s *sqlExecutionStore) Get(ctx context.Context, actionID string) (execution.Record, error) {
	return getExecution(ctx, s.db, s.driver, strings.TrimSpace(actionID))
}

func (s *sqlExecutionStore) ListIntent(ctx context.Context, intentID string) ([]execution.Record, error) {
	rows, err := s.db.QueryContext(ctx, bindSQL(s.driver, selectExecution+` WHERE current.intent_id = ? ORDER BY current.sequence_no`), strings.TrimSpace(intentID))
	if err != nil {
		return nil, fmt.Errorf("list intent action executions: %w", err)
	}
	defer rows.Close()
	var records []execution.Record
	for rows.Next() {
		record, err := scanExecution(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate intent action executions: %w", err)
	}
	return records, nil
}

func (s *sqlExecutionStore) requireOwnedUpdate(ctx context.Context, actionID string, result sql.Result) (execution.Record, error) {
	affected, err := result.RowsAffected()
	if err != nil {
		return execution.Record{}, fmt.Errorf("read action execution update result: %w", err)
	}
	if affected != 1 {
		return execution.Record{}, execution.ErrLeaseNotOwned
	}
	return s.Get(ctx, actionID)
}

const selectExecution = `SELECT
    current.incident_id, current.intent_id, current.action_id, current.action_digest,
    current.sequence_no, current.tool_name, current.idempotency_key, current.status,
    current.owner_id, current.lease_until_unix_ns, current.next_poll_at_unix_ns,
    current.operation_deadline_unix_ns, current.attempt, current.operation_id,
    current.operation_status, current.last_error, current.created_at_unix_ns,
    current.updated_at_unix_ns, current.finished_at_unix_ns
FROM action_executions current`

type sqlQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getExecution(ctx context.Context, queryer sqlQueryer, driver Driver, actionID string) (execution.Record, error) {
	return scanExecution(queryer.QueryRowContext(ctx, bindSQL(driver, selectExecution+` WHERE current.action_id = ?`), actionID))
}

func scanExecution(row scanner) (execution.Record, error) {
	var record execution.Record
	var leaseUntil, nextPollAt, operationDeadline, finishedAt sql.NullInt64
	var createdAt, updatedAt int64
	if err := row.Scan(
		&record.IncidentID, &record.IntentID, &record.ActionID, &record.ActionDigest,
		&record.Sequence, &record.ToolName, &record.IdempotencyKey, &record.Status,
		&record.OwnerID, &leaseUntil, &nextPollAt, &operationDeadline, &record.Attempt, &record.OperationID,
		&record.OperationStatus, &record.LastError, &createdAt, &updatedAt, &finishedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return execution.Record{}, execution.ErrNotFound
		}
		return execution.Record{}, fmt.Errorf("scan action execution: %w", err)
	}
	record.CreatedAt = time.Unix(0, createdAt).UTC()
	record.UpdatedAt = time.Unix(0, updatedAt).UTC()
	if leaseUntil.Valid {
		value := time.Unix(0, leaseUntil.Int64).UTC()
		record.LeaseUntil = &value
	}
	if nextPollAt.Valid {
		value := time.Unix(0, nextPollAt.Int64).UTC()
		record.NextPollAt = &value
	}
	if operationDeadline.Valid {
		value := time.Unix(0, operationDeadline.Int64).UTC()
		record.OperationDeadline = &value
	}
	if finishedAt.Valid {
		value := time.Unix(0, finishedAt.Int64).UTC()
		record.FinishedAt = &value
	}
	return record, nil
}

func validatePollSchedule(schedule execution.PollSchedule) error {
	if schedule.NextPollAt.IsZero() || schedule.OperationDeadline.IsZero() {
		return fmt.Errorf("next poll time and operation deadline are required")
	}
	if schedule.NextPollAt.After(schedule.OperationDeadline) {
		return fmt.Errorf("next poll time must not be after operation deadline")
	}
	return nil
}

func validateExecutionSpec(spec execution.Spec) error {
	for field, value := range map[string]string{
		"incident_id": spec.IncidentID, "intent_id": spec.IntentID, "action_id": spec.ActionID,
		"action_digest": spec.ActionDigest, "tool_name": spec.ToolName, "idempotency_key": spec.IdempotencyKey,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("action execution %s is required", field)
		}
	}
	if spec.Sequence <= 0 {
		return fmt.Errorf("action execution sequence must be positive")
	}
	return nil
}

func bindSQL(driver Driver, query string) string {
	if driver != DriverPostgres {
		return query
	}
	return bindPostgres(query)
}
