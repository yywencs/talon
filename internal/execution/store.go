// Package execution 定义 Plan Action 的持久化执行状态与租约接口。
package execution

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound      = errors.New("action execution not found")
	ErrConflict      = errors.New("action execution conflicts with persisted data")
	ErrNoClaimable   = errors.New("no action execution is currently claimable")
	ErrLeaseNotOwned = errors.New("action execution lease is not owned by worker")
)

// Status 是 Talon 对一个 Action 的确定性执行状态。
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusUnknown   Status = "unknown"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

// Spec 是从冻结 Plan 派生的不可变执行说明。
type Spec struct {
	IncidentID     string `json:"incident_id"`
	PlanID         string `json:"plan_id"`
	ActionID       string `json:"action_id"`
	ActionDigest   string `json:"action_digest"`
	Sequence       int    `json:"sequence"`
	ToolName       string `json:"tool_name"`
	IdempotencyKey string `json:"idempotency_key"`
}

// Record 保存租约、Platform Operation 和最终结果。
type Record struct {
	Spec
	Status            Status     `json:"status"`
	OwnerID           string     `json:"owner_id,omitempty"`
	LeaseUntil        *time.Time `json:"lease_until,omitempty"`
	NextPollAt        *time.Time `json:"next_poll_at,omitempty"`
	OperationDeadline *time.Time `json:"operation_deadline,omitempty"`
	Attempt           int        `json:"attempt"`
	OperationID       string     `json:"operation_id,omitempty"`
	OperationStatus   string     `json:"operation_status,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`
}

// PollSchedule 描述异步 Operation 的下次查询时间和最长等待期限。
type PollSchedule struct {
	NextPollAt        time.Time
	OperationDeadline time.Time
}

// Claim 描述 Worker 申请执行租约的参数。
type Claim struct {
	PlanID        string
	OwnerID       string
	LeaseDuration time.Duration
}

// Store 定义执行顺序、租约抢占和 Operation 对账所需的持久化能力。
type Store interface {
	Prepare(ctx context.Context, specs []Spec) ([]Record, error)
	ClaimNext(ctx context.Context, claim Claim) (Record, error)
	Renew(ctx context.Context, actionID, ownerID string, leaseDuration time.Duration) (Record, error)
	RecordOperation(ctx context.Context, actionID, ownerID, operationID, operationStatus string, schedule PollSchedule) (Record, error)
	MarkUnknown(ctx context.Context, actionID, ownerID, operationID, operationStatus, message string, schedule PollSchedule) (Record, error)
	Complete(ctx context.Context, actionID, ownerID, operationID, operationStatus string, status Status, message string) (Record, error)
	Get(ctx context.Context, actionID string) (Record, error)
	ListPlan(ctx context.Context, planID string) ([]Record, error)
}
