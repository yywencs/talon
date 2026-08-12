package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/wen/opentalon/internal/approval"
)

type sqlApprovalStore struct {
	db     *sql.DB
	driver Driver
	now    func() time.Time
}

var _ approval.Store = (*sqlApprovalStore)(nil)

func newSQLApprovalStore(db *sql.DB, driver Driver) *sqlApprovalStore {
	return &sqlApprovalStore{db: db, driver: driver, now: time.Now}
}

func (s *sqlApprovalStore) Create(ctx context.Context, request approval.Request) (approval.Request, error) {
	if err := validateApprovalCreate(request); err != nil {
		return approval.Request{}, err
	}
	arguments, err := json.Marshal(request.Arguments)
	if err != nil {
		return approval.Request{}, fmt.Errorf("marshal approval arguments: %w", err)
	}
	request.Status = approval.StatusPending
	if request.RequestedAt.IsZero() {
		request.RequestedAt = s.now().UTC()
	}
	query := `INSERT INTO approval_requests (
    id, incident_id, plan_id, action_id, action_digest, dry_run_operation_id,
    tool_name, arguments_json, risk, policy_reason, status, requested_at_unix_ns
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?)
ON CONFLICT DO NOTHING`
	if s.driver == DriverPostgres {
		query = `INSERT INTO approval_requests (
    id, incident_id, plan_id, action_id, action_digest, dry_run_operation_id,
    tool_name, arguments_json, risk, policy_reason, status, requested_at_unix_ns
) VALUES (?, ?, ?, ?, ?, ?, ?, CAST(? AS JSONB), ?, ?, 'pending', ?)
ON CONFLICT DO NOTHING`
	}
	_, err = s.db.ExecContext(ctx, s.bind(query),
		request.ID, request.IncidentID, request.PlanID, request.ActionID, request.ActionDigest,
		request.DryRunOperationID, request.ToolName, string(arguments), request.Risk,
		request.PolicyReason, request.RequestedAt.UnixNano(),
	)
	if err != nil {
		return approval.Request{}, fmt.Errorf("create approval request: %w", err)
	}
	persisted, err := s.Get(ctx, request.ID)
	if err != nil {
		return approval.Request{}, err
	}
	if !sameImmutableApproval(persisted, request, string(arguments)) {
		return approval.Request{}, fmt.Errorf("%w: approval ID %q is already bound to different action content", approval.ErrConflict, request.ID)
	}
	return persisted, nil
}

func (s *sqlApprovalStore) Get(ctx context.Context, id string) (approval.Request, error) {
	row := s.db.QueryRowContext(ctx, s.bind(selectApproval+` WHERE id = ?`), strings.TrimSpace(id))
	return scanApproval(row)
}

func (s *sqlApprovalStore) ListPending(ctx context.Context) ([]approval.Request, error) {
	rows, err := s.db.QueryContext(ctx, selectApproval+` WHERE status = 'pending' ORDER BY requested_at_unix_ns, id`)
	if err != nil {
		return nil, fmt.Errorf("list pending approvals: %w", err)
	}
	defer rows.Close()
	var result []approval.Request
	for rows.Next() {
		item, scanErr := scanApproval(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending approvals: %w", err)
	}
	return result, nil
}

func (s *sqlApprovalStore) Decide(ctx context.Context, decision approval.Decision) (approval.Request, error) {
	if err := validateApprovalDecision(decision); err != nil {
		return approval.Request{}, err
	}
	decidedAt := s.now().UTC()
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE approval_requests
SET status = ?, decided_at_unix_ns = ?, decided_by = ?, decision_reason = ?
WHERE id = ? AND plan_id = ? AND action_id = ? AND action_digest = ? AND status = 'pending'`),
		decision.Status, decidedAt.UnixNano(), decision.DecidedBy, decision.DecisionReason,
		decision.ID, decision.PlanID, decision.ActionID, decision.ActionDigest,
	)
	if err != nil {
		return approval.Request{}, fmt.Errorf("decide approval request: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return approval.Request{}, fmt.Errorf("read approval decision result: %w", err)
	}
	persisted, err := s.Get(ctx, decision.ID)
	if err != nil {
		return approval.Request{}, err
	}
	if affected == 1 {
		return persisted, nil
	}
	if persisted.PlanID != decision.PlanID || persisted.ActionID != decision.ActionID || persisted.ActionDigest != decision.ActionDigest {
		return approval.Request{}, fmt.Errorf("%w: approval decision does not match persisted action", approval.ErrConflict)
	}
	if persisted.Status == decision.Status && persisted.DecidedBy == decision.DecidedBy && persisted.DecisionReason == decision.DecisionReason {
		return persisted, nil
	}
	return approval.Request{}, fmt.Errorf("%w: approval %q was decided by %q", approval.ErrAlreadyDecided, decision.ID, persisted.DecidedBy)
}

func (s *sqlApprovalStore) bind(query string) string {
	return bindSQL(s.driver, query)
}

func bindPostgres(query string) string {
	var result strings.Builder
	index := 1
	for _, character := range query {
		if character == '?' {
			result.WriteByte('$')
			result.WriteString(strconv.Itoa(index))
			index++
		} else {
			result.WriteRune(character)
		}
	}
	return result.String()
}

const selectApproval = `SELECT
    id, incident_id, plan_id, action_id, action_digest, dry_run_operation_id,
    tool_name, arguments_json, risk, policy_reason, status, requested_at_unix_ns,
    decided_at_unix_ns, decided_by, decision_reason
FROM approval_requests`

type scanner interface {
	Scan(dest ...any) error
}

func scanApproval(row scanner) (approval.Request, error) {
	var item approval.Request
	var arguments []byte
	var requestedAt int64
	var decidedAt sql.NullInt64
	if err := row.Scan(
		&item.ID, &item.IncidentID, &item.PlanID, &item.ActionID, &item.ActionDigest,
		&item.DryRunOperationID, &item.ToolName, &arguments, &item.Risk, &item.PolicyReason,
		&item.Status, &requestedAt, &decidedAt, &item.DecidedBy, &item.DecisionReason,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return approval.Request{}, approval.ErrNotFound
		}
		return approval.Request{}, fmt.Errorf("scan approval request: %w", err)
	}
	if err := json.Unmarshal(arguments, &item.Arguments); err != nil {
		return approval.Request{}, fmt.Errorf("decode persisted approval arguments: %w", err)
	}
	item.RequestedAt = time.Unix(0, requestedAt).UTC()
	if decidedAt.Valid {
		value := time.Unix(0, decidedAt.Int64).UTC()
		item.DecidedAt = &value
	}
	return item, nil
}

func validateApprovalCreate(value approval.Request) error {
	for field, content := range map[string]string{
		"id": value.ID, "incident_id": value.IncidentID, "plan_id": value.PlanID,
		"action_id": value.ActionID, "action_digest": value.ActionDigest,
		"dry_run_operation_id": value.DryRunOperationID, "tool_name": value.ToolName, "risk": value.Risk,
	} {
		if strings.TrimSpace(content) == "" {
			return fmt.Errorf("approval request %s is required", field)
		}
	}
	return nil
}

func validateApprovalDecision(value approval.Decision) error {
	for field, content := range map[string]string{
		"id": value.ID, "plan_id": value.PlanID, "action_id": value.ActionID,
		"action_digest": value.ActionDigest, "decided_by": value.DecidedBy,
	} {
		if strings.TrimSpace(content) == "" {
			return fmt.Errorf("approval decision %s is required", field)
		}
	}
	if value.Status != approval.StatusApproved && value.Status != approval.StatusRejected {
		return fmt.Errorf("approval decision status must be approved or rejected")
	}
	if value.Status == approval.StatusRejected && strings.TrimSpace(value.DecisionReason) == "" {
		return fmt.Errorf("rejected approval decision reason is required")
	}
	return nil
}

func sameImmutableApproval(persisted, submitted approval.Request, submittedArguments string) bool {
	persistedArguments, err := json.Marshal(persisted.Arguments)
	return err == nil && persisted.IncidentID == submitted.IncidentID && persisted.PlanID == submitted.PlanID &&
		persisted.ActionID == submitted.ActionID && persisted.ActionDigest == submitted.ActionDigest &&
		persisted.DryRunOperationID == submitted.DryRunOperationID && persisted.ToolName == submitted.ToolName &&
		string(persistedArguments) == submittedArguments && persisted.Risk == submitted.Risk &&
		persisted.PolicyReason == submitted.PolicyReason
}
