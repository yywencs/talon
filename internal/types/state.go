package types

import (
	"sync"

	"github.com/google/uuid"
)

// ExecutionStatus 定义会话执行状态
type ExecutionStatus string

const (
	StatusIdle     ExecutionStatus = "idle"
	StatusRunning  ExecutionStatus = "running"
	StatusFinished ExecutionStatus = "finished"
	StatusError    ExecutionStatus = "error"
	StatusStuck    ExecutionStatus = "stuck"

	StatusPaused            ExecutionStatus = "paused"
	StatusWaitingForConfirm ExecutionStatus = "waiting_for_confirmation"
)

type SessionState struct {
	mu sync.RWMutex

	ID             string
	PersistenceDir string

	Status        ExecutionStatus
	MaxIterations int

	IterationCount int
	AgentState     Agent

	Events *EventLog
}

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
		Events:         NewEventLog(),
	}
}
