package approval

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStore 使用单个 SQLite 文件持久化审批收件箱。
type SQLiteStore struct {
	db  *sql.DB
	now func() time.Time
}

// NewSQLiteStore 打开数据库并自动创建审批表。path 的父目录不存在时会自动创建。
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("approval sqlite path is required")
	}
	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create approval database directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open approval sqlite database: %w", err)
	}
	// SQLite 写入串行化；限制连接数也能让 :memory: 数据库始终使用同一连接。
	db.SetMaxOpenConns(1)
	store := &SQLiteStore{db: db, now: time.Now}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close 关闭 SQLite 连接。
func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteStore) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS approval_requests (
    id                    TEXT PRIMARY KEY,
    incident_id           TEXT NOT NULL,
    plan_id               TEXT NOT NULL,
    action_id             TEXT NOT NULL,
    action_digest         TEXT NOT NULL,
    dry_run_operation_id  TEXT NOT NULL,
    tool_name             TEXT NOT NULL,
    arguments_json        TEXT NOT NULL,
    risk                  TEXT NOT NULL,
    policy_reason         TEXT NOT NULL DEFAULT '',
    status                TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'rejected')),
    requested_at_unix_ns  INTEGER NOT NULL,
    decided_at_unix_ns    INTEGER,
    decided_by            TEXT NOT NULL DEFAULT '',
    decision_reason       TEXT NOT NULL DEFAULT '',
    UNIQUE (plan_id, action_id)
);
CREATE INDEX IF NOT EXISTS idx_approval_requests_pending
    ON approval_requests(status, requested_at_unix_ns, id);`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate approval sqlite database: %w", err)
	}
	return nil
}

// Create 幂等创建 pending 审批单；相同 ID 但内容不同会返回 ErrConflict。
func (s *SQLiteStore) Create(ctx context.Context, request Request) (Request, error) {
	if err := validateCreate(request); err != nil {
		return Request{}, err
	}
	arguments, err := json.Marshal(request.Arguments)
	if err != nil {
		return Request{}, fmt.Errorf("marshal approval arguments: %w", err)
	}
	request.Status = StatusPending
	if request.RequestedAt.IsZero() {
		request.RequestedAt = s.now().UTC()
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO approval_requests (
    id, incident_id, plan_id, action_id, action_digest, dry_run_operation_id,
    tool_name, arguments_json, risk, policy_reason, status, requested_at_unix_ns
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?)
ON CONFLICT DO NOTHING`,
		request.ID, request.IncidentID, request.PlanID, request.ActionID, request.ActionDigest,
		request.DryRunOperationID, request.ToolName, string(arguments), request.Risk,
		request.PolicyReason, request.RequestedAt.UnixNano(),
	)
	if err != nil {
		return Request{}, fmt.Errorf("create approval request: %w", err)
	}
	persisted, err := s.Get(ctx, request.ID)
	if err != nil {
		return Request{}, err
	}
	if !sameImmutableRequest(persisted, request, string(arguments)) {
		return Request{}, fmt.Errorf("%w: approval ID %q is already bound to different action content", ErrConflict, request.ID)
	}
	return persisted, nil
}

// Get 按审批单 ID 查询记录。
func (s *SQLiteStore) Get(ctx context.Context, id string) (Request, error) {
	row := s.db.QueryRowContext(ctx, selectApproval+` WHERE id = ?`, strings.TrimSpace(id))
	return scanApproval(row)
}

// ListPending 返回按创建时间排序的待审批 Action。
func (s *SQLiteStore) ListPending(ctx context.Context) ([]Request, error) {
	rows, err := s.db.QueryContext(ctx, selectApproval+` WHERE status = 'pending' ORDER BY requested_at_unix_ns, id`)
	if err != nil {
		return nil, fmt.Errorf("list pending approvals: %w", err)
	}
	defer rows.Close()
	var result []Request
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

// Decide 只允许 pending -> approved/rejected 的一次性原子更新。
// 完全相同的重复请求按幂等成功处理，其他重复决定返回 ErrAlreadyDecided。
func (s *SQLiteStore) Decide(ctx context.Context, decision Decision) (Request, error) {
	if err := validateDecision(decision); err != nil {
		return Request{}, err
	}
	decidedAt := s.now().UTC()
	result, err := s.db.ExecContext(ctx, `
UPDATE approval_requests
SET status = ?, decided_at_unix_ns = ?, decided_by = ?, decision_reason = ?
WHERE id = ? AND plan_id = ? AND action_id = ? AND action_digest = ? AND status = 'pending'`,
		decision.Status, decidedAt.UnixNano(), decision.DecidedBy, decision.DecisionReason,
		decision.ID, decision.PlanID, decision.ActionID, decision.ActionDigest,
	)
	if err != nil {
		return Request{}, fmt.Errorf("decide approval request: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Request{}, fmt.Errorf("read approval decision result: %w", err)
	}
	persisted, getErr := s.Get(ctx, decision.ID)
	if getErr != nil {
		return Request{}, getErr
	}
	if affected == 1 {
		return persisted, nil
	}
	if persisted.PlanID != decision.PlanID || persisted.ActionID != decision.ActionID || persisted.ActionDigest != decision.ActionDigest {
		return Request{}, fmt.Errorf("%w: approval decision does not match persisted action", ErrConflict)
	}
	if persisted.Status == decision.Status && persisted.DecidedBy == decision.DecidedBy && persisted.DecisionReason == decision.DecisionReason {
		return persisted, nil
	}
	return Request{}, fmt.Errorf("%w: approval %q was decided by %q", ErrAlreadyDecided, decision.ID, persisted.DecidedBy)
}

const selectApproval = `SELECT
    id, incident_id, plan_id, action_id, action_digest, dry_run_operation_id,
    tool_name, arguments_json, risk, policy_reason, status, requested_at_unix_ns,
    decided_at_unix_ns, decided_by, decision_reason
FROM approval_requests`

type scanner interface {
	Scan(dest ...any) error
}

func scanApproval(row scanner) (Request, error) {
	var item Request
	var arguments string
	var requestedAt int64
	var decidedAt sql.NullInt64
	if err := row.Scan(
		&item.ID, &item.IncidentID, &item.PlanID, &item.ActionID, &item.ActionDigest,
		&item.DryRunOperationID, &item.ToolName, &arguments, &item.Risk, &item.PolicyReason,
		&item.Status, &requestedAt, &decidedAt, &item.DecidedBy, &item.DecisionReason,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Request{}, ErrNotFound
		}
		return Request{}, fmt.Errorf("scan approval request: %w", err)
	}
	if err := json.Unmarshal([]byte(arguments), &item.Arguments); err != nil {
		return Request{}, fmt.Errorf("decode persisted approval arguments: %w", err)
	}
	item.RequestedAt = time.Unix(0, requestedAt).UTC()
	if decidedAt.Valid {
		value := time.Unix(0, decidedAt.Int64).UTC()
		item.DecidedAt = &value
	}
	return item, nil
}

func validateCreate(value Request) error {
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

func validateDecision(value Decision) error {
	for field, content := range map[string]string{
		"id": value.ID, "plan_id": value.PlanID, "action_id": value.ActionID,
		"action_digest": value.ActionDigest, "decided_by": value.DecidedBy,
	} {
		if strings.TrimSpace(content) == "" {
			return fmt.Errorf("approval decision %s is required", field)
		}
	}
	if value.Status != StatusApproved && value.Status != StatusRejected {
		return fmt.Errorf("approval decision status must be approved or rejected")
	}
	if value.Status == StatusRejected && strings.TrimSpace(value.DecisionReason) == "" {
		return fmt.Errorf("rejected approval decision reason is required")
	}
	return nil
}

func sameImmutableRequest(persisted, submitted Request, submittedArguments string) bool {
	persistedArguments, err := json.Marshal(persisted.Arguments)
	return err == nil && persisted.IncidentID == submitted.IncidentID && persisted.PlanID == submitted.PlanID &&
		persisted.ActionID == submitted.ActionID && persisted.ActionDigest == submitted.ActionDigest &&
		persisted.DryRunOperationID == submitted.DryRunOperationID && persisted.ToolName == submitted.ToolName &&
		string(persistedArguments) == submittedArguments && persisted.Risk == submitted.Risk &&
		persisted.PolicyReason == submitted.PolicyReason
}
