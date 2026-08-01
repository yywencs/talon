package core

import (
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/wen/opentalon/internal/context"
	"github.com/wen/opentalon/internal/types"
)

// ExecutionStatus 定义会话执行状态的枚举，驱动 Session 的状态机流转。
type ExecutionStatus string

const (
	// StatusIdle 表示会话刚创建，尚未开始运行。
	StatusIdle     ExecutionStatus = "idle"
	// StatusRunning 表示 Agent 正在执行推理循环。
	StatusRunning  ExecutionStatus = "running"
	// StatusFinished 表示 Agent 主动触发了 finish，会话正常终止。
	StatusFinished ExecutionStatus = "finished"
	// StatusError 表示会话因不可恢复错误而停止。
	StatusError    ExecutionStatus = "error"
	// StatusStuck 表示会话超过最大迭代次数仍未结束。
	StatusStuck    ExecutionStatus = "stuck"

	// StatusPaused 表示会话被外部暂停，等待恢复信号。
	StatusPaused            ExecutionStatus = "paused"
	// StatusWaitingForConfirm 表示会话等待用户确认高风险动作后继续。
	StatusWaitingForConfirm ExecutionStatus = "waiting_for_confirmation"
)

// SessionState 持有会话的完整运行时状态：执行状态机、事件历史、
// token 预算和迭代限制。由 Session 持有，驱动每一轮推理循环。
type SessionState struct {
	mu sync.RWMutex

	ID             string
	PersistenceDir string

	Status     ExecutionStatus
	AgentState Agent

	RunTimeout     time.Duration
	MaxIterations  int
	IterationCount int

	Events       *types.EventLog
	TokenTracker *context.TokenTracker
}

// NewSessionState 创建一个处于 idle 状态的会话状态，默认最大迭代 1000 次。
func NewSessionState(agent Agent, persistenceDir string) *SessionState {
	sessionID, err := uuid.NewV7()
	if err != nil {
		panic(err)
	}

	return &SessionState{
		ID:             sessionID.String(),
		PersistenceDir: persistenceDir,
		Status:         StatusIdle,
		MaxIterations:  1000,
		AgentState:     agent,
		Events:         types.NewEventLog(),
		TokenTracker:   context.NewTokenTracker(),
	}
}
