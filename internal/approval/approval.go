// Package approval 定义需要人工处理的 Action 级审批单及其持久化端口。
package approval

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound       = errors.New("approval request not found")
	ErrAlreadyDecided = errors.New("approval request already decided")
	ErrConflict       = errors.New("approval request conflicts with persisted data")
)

// Status 表示审批单的持久化状态。
type Status string

const (
	StatusPending  Status = "pending"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
)

// Request 是审批页面或命令行需要展示的一张 Action 审批单。
type Request struct {
	ID                string         `json:"id"`
	IncidentID        string         `json:"incident_id"`
	IntentID          string         `json:"intent_id"`
	ActionID          string         `json:"action_id"`
	ActionDigest      string         `json:"action_digest"`
	DryRunOperationID string         `json:"dry_run_operation_id"`
	ToolName          string         `json:"tool_name"`
	Arguments         map[string]any `json:"arguments"`
	Risk              string         `json:"risk"`
	PolicyReason      string         `json:"policy_reason,omitempty"`
	Status            Status         `json:"status"`
	RequestedAt       time.Time      `json:"requested_at"`
	DecidedAt         *time.Time     `json:"decided_at,omitempty"`
	DecidedBy         string         `json:"decided_by,omitempty"`
	DecisionReason    string         `json:"decision_reason,omitempty"`
}

// Decision 是人工对一张待审批单提交的不可变决定。
type Decision struct {
	ID             string `json:"id"`
	IntentID       string `json:"intent_id"`
	ActionID       string `json:"action_id"`
	ActionDigest   string `json:"action_digest"`
	Status         Status `json:"status"`
	DecidedBy      string `json:"decided_by"`
	DecisionReason string `json:"decision_reason,omitempty"`
}

// Store 定义审批收件箱需要的最小持久化能力。
type Store interface {
	Create(ctx context.Context, request Request) (Request, error)
	Get(ctx context.Context, id string) (Request, error)
	ListPending(ctx context.Context) ([]Request, error)
	Decide(ctx context.Context, decision Decision) (Request, error)
}

// RequestID 返回 Action 审批单的确定性 ID，重复 Policy 评估不会创建重复记录。
func RequestID(actionID string) string {
	return actionID + ":approval"
}
