// Package workflow 提供 Incident 生命周期的确定性状态机和操作白名单。
package workflow

import "fmt"

// State 表示一个 Incident 当前所处的生命周期阶段。
type State string

const (
	// StateProtected（保护完成，初始态）：控制器已识别 Incident 并完成流量保护
	//（降权/熔断），调查尚未开始，Agent 还未介入。
	StateProtected State = "protected"
	// StateInvestigating（调查中）：Agent 通过只读工具收集证据、按需加载/卸载
	// 诊断 Skill 形成根因假设；提交 ExecutionIntent 前一直停留在此状态。
	StateInvestigating State = "investigating"
	// StateValidating（校验中）：Agent 已提交 ExecutionIntent，Workflow 正在对
	// 当前 Stage 的每个 Action 做无副作用的 Dry Run 与风险策略评估。
	StateValidating State = "validating"
	// StateAwaitingApproval（等待审批）：Dry Run 通过但存在需要人工审批的风险
	// 动作，流程暂停在审批门禁，等待人工授权或拒绝。
	StateAwaitingApproval State = "awaiting_approval"
	// StateExecuting（受控执行中）：当前 Stage 的 Action 已授权，正由执行器以
	// 租约和幂等键受控执行异步 Operation；期间 Agent 只能查询 Operation 状态
	// 或请求升级。
	StateExecuting State = "executing"
	// StateCheckpoint（检查点判定）：当前 Stage 全部 Action 执行完成，正按
	// checkpoint_policy 做确定性结果判定（继续下一 Stage / 判定成功 / 唤回
	// Agent / 失败 / 升级 / 阻塞）。
	StateCheckpoint State = "checkpoint"
	// StateResolved（已解决，最终终态）：所有 Stage 均判定 succeeded，Incident
	// 修复完成，Workflow 不再有任何出边。
	StateResolved State = "resolved"
	// StateEscalated（已升级，暂停终态）：Agent 或 Workflow 停止自治并交人工
	// 接管；升级前状态记录在 suspended_state，人工处理后可通过 human_resumed
	// 恢复调查。
	StateEscalated State = "escalated"
	// StateFailed（已失败，终态）：修复链路被确定性判定失败（可重试失败收敛、
	// Stage 数或 Agent 唤回限额耗尽、Checkpoint 判定失败），不再自主执行。
	StateFailed State = "failed"
	// StateBlocked（已阻塞，终态）：执行被授权/审批/策略明确拒绝或 Checkpoint
	// 判定无法继续，需要外部介入才能解锁。
	StateBlocked State = "blocked"
)

var validStates = map[State]struct{}{
	StateProtected:        {},
	StateInvestigating:    {},
	StateValidating:       {},
	StateAwaitingApproval: {},
	StateExecuting:        {},
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
	ActorAgent      Actor = "agent"      // LLM Agent：调查、提交意图、升级（不可直接执行写操作）
	ActorWorkflow   Actor = "workflow"   // 状态机/Harness 自身：Dry Run、策略评估、Checkpoint 判定
	ActorController Actor = "controller" // 编排器：开案（start_investigation）等流程推进
	ActorHuman      Actor = "human"      // 人工：审批授权、拒绝意图、恢复已升级的 Incident
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
