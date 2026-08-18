// Package workflow 提供 Incident 生命周期的确定性状态机和操作白名单。
package workflow

import "fmt"

// State 表示一个 Incident 当前所处的生命周期阶段。
type State string

const (
	StateProtected        State = "protected"
	StateInvestigating    State = "investigating"
	StatePlanned          State = "planned"
	StateAwaitingApproval State = "awaiting_approval"
	StateRemediating      State = "remediating"
	StateResolved         State = "resolved"
	StateEscalated        State = "escalated"
	StateCheckpoint       State = "checkpoint"
	StateFailed           State = "failed"
	StateBlocked          State = "blocked"
)

var validStates = map[State]struct{}{
	StateProtected:        {},
	StateInvestigating:    {},
	StatePlanned:          {},
	StateAwaitingApproval: {},
	StateRemediating:      {},
	StateResolved:         {},
	StateEscalated:        {},
	StateCheckpoint:       {},
	StateFailed:           {},
	StateBlocked:          {},
}

// Valid 报告状态值是否属于 IncidentWorkflow。
func (s State) Valid() bool {
	_, ok := validStates[s]
	return ok
}

// Terminal 报告状态是否禁止 Workflow 继续自主执行生产写操作。
// escalated 可以由人工显式恢复，因此它是暂停终态；resolved 是最终终态。
func (s State) Terminal() bool {
	return s == StateResolved || s == StateEscalated || s == StateFailed || s == StateBlocked
}

func validateState(state State) error {
	if !state.Valid() {
		return fmt.Errorf("unknown incident state %q", state)
	}
	return nil
}

// Actor 表示触发状态事件或请求执行操作的一方。
type Actor string

const (
	ActorAgent      Actor = "agent"
	ActorWorkflow   Actor = "workflow"
	ActorController Actor = "controller"
	ActorHuman      Actor = "human"
)

func (a Actor) valid() bool {
	switch a {
	case ActorAgent, ActorWorkflow, ActorController, ActorHuman:
		return true
	default:
		return false
	}
}

type actorSet map[Actor]struct{}

func actors(values ...Actor) actorSet {
	result := make(actorSet, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}
