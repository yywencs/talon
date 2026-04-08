package core

import (
	"testing"

	"github.com/wen/opentalon/internal/types"
)

type stubAgent struct {
	actions []types.Action
	calls   int
}

func (a *stubAgent) Step(state *types.State) (types.Action, error) {
	if a.calls >= len(a.actions) {
		return nil, nil
	}

	action := a.actions[a.calls]
	a.calls++
	return action, nil
}

func TestControllerStepsAgainAfterObservation(t *testing.T) {
	bus := &EventBus{}
	agent := &stubAgent{
		actions: []types.Action{
			&types.CmdRunAction{Command: "first"},
			&types.CmdRunAction{Command: "second"},
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
		action, ok := evt.(*types.CmdRunAction)
		if !ok || action.GetBase().Source != types.SourceAgent {
			return
		}
		if action.Command != "first" {
			return
		}

		bus.Publish(&types.CmdOutputObservation{
			BaseEvent: types.BaseEvent{Cause: action.GetBase().ID},
			Content:   "ok",
		})
	})

	bus.Publish(&types.MessageAction{
		BaseEvent: types.BaseEvent{Source: types.SourceUser},
		Content:   "task",
	})

	if agent.calls != 2 {
		t.Fatalf("expected agent to step twice, got %d", agent.calls)
	}
	if state.AgentState != types.StateRunning {
		t.Fatalf("expected agent state to be running, got %s", state.AgentState)
	}

	pending, ok := state.PendingAction.(*types.CmdRunAction)
	if !ok {
		t.Fatalf("expected pending action to be a command action, got %T", state.PendingAction)
	}
	if pending.Command != "second" {
		t.Fatalf("expected second action to remain pending, got %q", pending.Command)
	}
	if len(state.History) != 4 {
		t.Fatalf("expected 4 history events, got %d", len(state.History))
	}
	if state.History[1].GetBase().ID == 0 {
		t.Fatal("expected published agent action to have a generated id")
	}
}

func TestControllerFinishesWithoutPendingAction(t *testing.T) {
	bus := &EventBus{}
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
