// State 是 Controller 和 Agent 之间共享的执行上下文。
// 它是整个事件循环的"单一真相来源"（Single Source of Truth）：
//   - AgentState 描述当前执行阶段（Loading/Running/AwaitingInput/Finished/Error）。
//   - History 记录所有已发生的事件，是 Agent 推理的上下文来源。
//   - PendingAction 是因果闭环的挂起槽位，确保 agent 在等观察结果期间不会被再次调用。
//
// 设计要点：
//   - State 应该只被 Controller 写入（除了 History 由 OnEvent 追加之外）。
//   - Agent 只读 State，不修改任何字段，这保证了 agent 的纯函数性质（给定相同 state，产生相同 action）。
//   - PendingAction 是单一槽位而非队列，这与"单流程串行步进"的设计决策一致。
package types

import "time"

// AgentState 描述 Agent 执行引擎的当前阶段。
// 每个状态的语义：
//   - StateLoading：系统刚启动，未收到任何用户消息。
//   - StateRunning：Agent 可以正常执行 step()。
//   - StateAwaitingInput：Agent 发送了 WaitForResponse=true 的消息，暂停循环等待用户输入。
//   - StateFinished：Agent 返回了 FinishAction，任务正常结束。
//   - StateError：Agent.Step() 返回了 error 或 nil action，任务异常终止。
type AgentState string

const (
	StateLoading       AgentState = "loading"
	StateRunning       AgentState = "running"
	StateAwaitingInput AgentState = "awaiting_user_input"
	StatePaused        AgentState = "paused"
	StateStopped       AgentState = "stopped"
	StateFinished      AgentState = "finished"
	StateRejected      AgentState = "rejected"
	StateError         AgentState = "error"

	// HITL (人在回路) 相关状态
	StateAwaitingConfirmation AgentState = "awaiting_user_confirmation"
	StateUserConfirmed        AgentState = "user_confirmed"
	StateUserRejected         AgentState = "user_rejected"
)

type State struct {
	AgentState    AgentState
	History       []Event
	PendingAction Action
	LastError     string
	CreatedAt     time.Time
}

// NewState 是 State 的构造器，初始化所有字段为安全的默认值。
// History 预分配为 nil（而不是空切片），append 会正常工作且没有不必要的内存分配。
// CreatedAt 记录会话创建时间，可用于计算总执行时长。
func NewState() *State {
	return &State{
		AgentState: StateLoading,
		History:    nil,
		CreatedAt:  time.Now(),
	}
}
