package workflow

import (
	"errors"
	"fmt"
	"sort"
)

var ErrAgentActionDenied = errors.New("incident agent action denied")

// AgentAction 是当前需要按状态控制、并可能映射为 Eino 工具的 Agent 动作。
// Workflow、Controller 和 Human 的内部动作由事件转换规则控制，不放在这里。
type AgentAction string

const (
	// AgentActionRead 代表当前状态允许的只读证据、状态或审计查询。
	AgentActionRead AgentAction = "read"
	// AgentActionSubmitPlan 代表 Agent 提交结构化 Plan，不代表直接执行修复。
	AgentActionSubmitPlan AgentAction = "submit_plan"
	// AgentActionQueryOperation 代表查询当前修复、探测或恢复 Operation 的状态。
	AgentActionQueryOperation AgentAction = "query_operation"
	// AgentActionEscalate 代表 Agent 停止自治并提交证据，请求人工接管。
	AgentActionEscalate AgentAction = "escalate"
)

type agentActionSet map[AgentAction]struct{}

func agentActions(values ...AgentAction) agentActionSet {
	result := make(agentActionSet, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

var stateAgentActions = map[State]agentActionSet{
	StateProtected: agentActions(
		AgentActionEscalate,
	),
	StateInvestigating: investigationActions(),
	StatePlanned: agentActions(
		AgentActionRead,
		AgentActionEscalate,
	),
	StateAwaitingApproval: agentActions(
		AgentActionRead,
		AgentActionEscalate,
	),
	StateRemediating: agentActions(
		AgentActionQueryOperation,
		AgentActionEscalate,
	),
	StateProbing: agentActions(
		AgentActionRead,
		AgentActionQueryOperation,
		AgentActionEscalate,
	),
	StateRecovering: agentActions(
		AgentActionRead,
		AgentActionQueryOperation,
		AgentActionEscalate,
	),
	StateReinvestigating: investigationActions(),
	StateCompensating: agentActions(
		AgentActionQueryOperation,
		AgentActionEscalate,
	),
	StateResolved: agentActions(
		AgentActionRead,
	),
	StateEscalated: agentActions(
		AgentActionRead,
	),
}

func investigationActions() agentActionSet {
	return agentActions(
		AgentActionRead,
		AgentActionQueryOperation,
		AgentActionSubmitPlan,
		AgentActionEscalate,
	)
}

// AuthorizeAgentAction 在工具真正执行前检查当前状态是否允许该 Agent 动作。
func (w *IncidentWorkflow) AuthorizeAgentAction(action AgentAction) error {
	if w == nil {
		return fmt.Errorf("%w: workflow is not initialized", ErrAgentActionDenied)
	}
	w.mu.RLock()
	state := w.state
	w.mu.RUnlock()
	return authorizeAgentAction(state, action)
}

func authorizeAgentAction(state State, action AgentAction) error {
	if _, allowed := stateAgentActions[state][action]; !allowed {
		return fmt.Errorf("%w: action %q is not allowed in state %q", ErrAgentActionDenied, action, state)
	}
	return nil
}

// AllowedAgentActions 返回当前状态应向 ToolOpsAgent 暴露的动作白名单，结果稳定排序。
func (w *IncidentWorkflow) AllowedAgentActions() []AgentAction {
	if w == nil {
		return nil
	}
	w.mu.RLock()
	state := w.state
	w.mu.RUnlock()

	policy := stateAgentActions[state]
	result := make([]AgentAction, 0, len(policy))
	for action := range policy {
		result = append(result, action)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
