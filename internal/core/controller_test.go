package core

import (
	"context"
	"testing"
	"time"

	toolpkg "github.com/wen/opentalon/internal/tool"
	"github.com/wen/opentalon/internal/types"
)

type stubAgent struct {
	actions []types.Action
	calls   int
}

func (a *stubAgent) Step(ctx context.Context, state *types.State) (types.Action, error) {
	if a.calls >= len(a.actions) {
		return nil, nil
	}

	action := a.actions[a.calls]
	a.calls++
	return action, nil
}

func TestControllerStepsAgainAfterObservation(t *testing.T) {
	bus := NewEventBus()
	bus.Start()
	defer bus.Stop()
	agent := &stubAgent{
		actions: []types.Action{
			&toolpkg.TerminalAction{Command: "first"},
			&toolpkg.TerminalAction{Command: "second"},
		},
	}
	state := &types.State{AgentState: types.StateLoading}
	controller := &Controller{
		bus:   bus,
		agent: agent,
		state: state,
	}

	bus.Subscribe(controller.OnEvent)
	bus.Subscribe(func(evt types.Event) {
		actionEvent, ok := evt.(*types.ActionEvent)
		if !ok || actionEvent.GetSource() != types.SourceAgent {
			return
		}
		action, ok := actionEvent.Action.(*toolpkg.TerminalAction)
		if !ok {
			return
		}
		if action.Command != "first" {
			return
		}

		waitFor(t, func() bool {
			return state.PendingAction != nil && state.PendingAction.GetID() == actionEvent.GetID()
		})

		bus.Publish(&types.ObservationEvent{
			BaseEvent: types.BaseEvent{
				Source: types.SourceEnvironment,
			},
			ActionID:    actionEvent.GetID(),
			ToolName:    "bash",
			Observation: toolpkg.NewTerminalObservation("first", "", nil, false, 0, "ok"),
		})
	})

	bus.Publish(&types.MessageAction{
		BaseEvent: types.BaseEvent{Source: types.SourceUser},
		Content:   "task",
	})

	waitFor(t, func() bool {
		if agent.calls != 2 {
			return false
		}
		if state.PendingAction == nil {
			return false
		}
		pending, ok := state.PendingAction.Action.(*toolpkg.TerminalAction)
		return ok && pending.Command == "second"
	})

	if agent.calls != 2 {
		t.Fatalf("expected agent to step twice, got %d", agent.calls)
	}
	if state.AgentState != types.StateRunning {
		t.Fatalf("expected agent state to be running, got %s", state.AgentState)
	}

	pending, ok := state.PendingAction.Action.(*toolpkg.TerminalAction)
	if !ok {
		t.Fatalf("expected pending action to be a command action, got %T", state.PendingAction)
	}
	if pending.Command != "second" {
		t.Fatalf("expected second action to remain pending, got %q", pending.Command)
	}
	if len(state.History) < 3 {
		t.Fatalf("expected at least 3 history events, got %d", len(state.History))
	}

	var hasUserMessage bool
	var hasFirstAction bool
	var hasObservation bool
	var hasSecondAction bool

	for _, evt := range state.History {
		switch e := evt.(type) {
		case *types.MessageAction:
			if e.GetSource() == types.SourceUser && e.Content == "task" {
				hasUserMessage = true
			}
		case *types.ActionEvent:
			terminalAction, ok := e.Action.(*toolpkg.TerminalAction)
			if !ok {
				continue
			}
			if terminalAction.Command == "first" {
				hasFirstAction = true
				if e.GetID() == "" {
					t.Fatal("expected published agent action to have a generated id")
				}
			}
			if terminalAction.Command == "second" {
				hasSecondAction = true
			}
		case *types.ObservationEvent:
			if e.ActionID != "" {
				hasObservation = true
			}
		}
	}

	if !hasUserMessage || !hasFirstAction || !hasObservation || !hasSecondAction {
		t.Fatalf("expected history to contain user message, first action, observation and second action, got %#v", state.History)
	}
}

func TestControllerFinishesWithoutPendingAction(t *testing.T) {
	bus := NewEventBus()
	bus.Start()
	defer bus.Stop()
	agent := &stubAgent{
		actions: []types.Action{
			&types.FinishAction{Result: "done"},
		},
	}
	state := &types.State{AgentState: types.StateLoading}
	controller := &Controller{
		bus:   bus,
		agent: agent,
		state: state,
	}

	bus.Subscribe(controller.OnEvent)
	bus.Publish(&types.MessageAction{
		BaseEvent: types.BaseEvent{Source: types.SourceUser},
		Content:   "task",
	})

	waitFor(t, func() bool {
		return agent.calls == 1 && state.AgentState == types.StateFinished && state.PendingAction == nil && len(state.History) == 2
	})

	if agent.calls != 1 {
		t.Fatalf("expected agent to step once, got %d", agent.calls)
	}
	if state.AgentState != types.StateFinished {
		t.Fatalf("expected agent state to be finished, got %s", state.AgentState)
	}
	if state.PendingAction != nil {
		t.Fatalf("expected no pending action after finish, got %T", state.PendingAction)
	}

	finish, ok := state.History[1].(*types.FinishAction)
	if !ok {
		t.Fatalf("expected second history event to be finish action, got %T", state.History[1])
	}
	if finish.Result != "done" {
		t.Fatalf("expected finish result to be %q, got %q", "done", finish.Result)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("condition not met before timeout")
}
