package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wen/opentalon/internal/types"
)

type fakeAgent struct {
	streamStepCalls int
}

func (a *fakeAgent) StreamStep(ctx context.Context, state *SessionState, onOutput func(types.AgentOutput)) (*types.AgentTurnResult, error) {
	a.streamStepCalls++
	return &types.AgentTurnResult{Finished: true}, nil
}

type blockingAgent struct {
	streamStepCalls int
}

func (a *blockingAgent) StreamStep(ctx context.Context, state *SessionState, onOutput func(types.AgentOutput)) (*types.AgentTurnResult, error) {
	a.streamStepCalls++
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRunMarksSessionStuckWhenMaxIterationsExceeded(t *testing.T) {
	t.Parallel()

	agent := &fakeAgent{}
	session := NewSession(agent, nil, t.TempDir())
	session.state.MaxIterations = 1
	session.state.IterationCount = 0
	session.state.Status = StatusRunning

	if err := session.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got := session.state.Status; got != StatusStuck {
		t.Fatalf("session status = %q, want %q", got, StatusStuck)
	}

	if agent.streamStepCalls != 0 {
		t.Fatalf("StreamStep() calls = %d, want 0", agent.streamStepCalls)
	}
}

func TestRunReturnsTimeoutErrorWhenRunTimeoutExceeded(t *testing.T) {
	t.Parallel()

	agent := &blockingAgent{}
	session := NewSession(agent, nil, t.TempDir())
	session.state.Status = StatusRunning
	session.state.RunTimeout = 20 * time.Millisecond
	session.state.MaxIterations = 100

	err := session.Run(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want deadline exceeded", err)
	}

	if got := session.state.Status; got != StatusStuck {
		t.Fatalf("session status = %q, want %q", got, StatusStuck)
	}

	if agent.streamStepCalls != 1 {
		t.Fatalf("StreamStep() calls = %d, want 1", agent.streamStepCalls)
	}
}
