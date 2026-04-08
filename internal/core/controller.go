package core

import (
	"fmt"

	"github.com/wen/opentalon/internal/agent"
	"github.com/wen/opentalon/internal/types"
)

// Controller 是事件循环的驱动引擎，所有状态转换和 agent 步进由它统一编排。
// 核心 invariant：
//   - step() 只能在 AgentState == Running 且 PendingAction == nil 时调用。
//   - 这两条 guard 确保 agent 不会在等待观察结果期间被再次调用，从而避免幻觉。
type Controller struct {
	bus   *EventBus
	agent agent.Agent
	state *types.State
}

// NewController 构造一个 Controller，将 agent 和 state 绑定到同一个事件总线。
// Controller 本身不创建 goroutine，驱动来自外部对 OnEvent 的调用。
func NewController(bus *EventBus, agent agent.Agent, state *types.State) *Controller {
	return &Controller{
		bus:   bus,
		agent: agent,
		state: state,
	}
}

// OnEvent 是总线的订阅回调，所有外部事件（用户输入、runtime 观察结果）都从这儿进入。
// 顺序：记录历史 → 处理事件副作用 → 判断是否推进 agent。
func (c *Controller) OnEvent(evt types.Event) {
	c.state.History = append(c.state.History, evt)
	fmt.Printf("Received Event: %v\n", evt)

	switch e := evt.(type) {
	case types.Action:
		fmt.Println("receive action event!")
		c.handleAction(e)
	case types.Observation:
		fmt.Println("receive observation event!")
		c.handleObservation(e)
	}

	if c.shouldStep(evt) {
		c.step()
	}
}

// step 将控制权移交给 agent。
// 两个 guard 必须同时满足：
//  1. AgentState == Running：非运行态直接跳过，避免在暂停/终止态继续调度。
//  2. PendingAction == nil：有未完成的 action 时阻塞，防止并发步进而产生上下文错乱。
func (c *Controller) step() {
	if c.state.AgentState != types.StateRunning {
		return
	}
	if c.state.PendingAction != nil {
		return
	}

	action, err := c.agent.Step(c.state)

	if err != nil {
		c.state.LastError = err.Error()
		c.state.AgentState = types.StateError
		return
	}
	if action == nil {
		c.state.LastError = "agent returned nil action"
		c.state.AgentState = types.StateError
		return
	}

	action.GetBase().Source = types.SourceAgent
	// 只有需要环境回执的动作才挂起；MessageAction 和 FinishAction 不阻塞循环。
	if requiresObservation(action) {
		c.state.PendingAction = action
	}

	c.bus.Publish(action)
}

// handleAction 根据 action 类型驱动状态机。
// 关键语义：Source 决定这条消息是"用户说的"还是"agent 说的"，
// 两者在状态转换中承担完全不同的角色。
func (c *Controller) handleAction(action types.Action) {
	switch a := action.(type) {
	case *types.MessageAction:
		c.handleMessageAction(a)
	case *types.FinishAction:
		c.handleFinishAction(a)
	case *types.CmdRunAction:
		// CmdRunAction 只有在 Source == Agent 时才挂起；
		// 用户也可以发 CmdRunAction（比如快捷命令），此时不需要等待观察结果。
		if a.GetBase().Source == types.SourceAgent {
			c.state.PendingAction = a
		}
	}
}

// handleMessageAction 处理用户消息和 agent 消息在状态转换上的差异。
// 用户消息：打破等待态/初始态，将状态切回运行。
// agent 消息：WaitForResponse=true 表示需要人工确认，切到AwaitingInput 态暂停循环。
func (c *Controller) handleMessageAction(action *types.MessageAction) {
	switch action.GetBase().Source {
	case types.SourceUser:
		if c.state.AgentState == types.StateLoading || c.state.AgentState == types.StateAwaitingInput {
			c.state.AgentState = types.StateRunning
		}
	case types.SourceAgent:
		if action.WaitForResponse {
			c.state.AgentState = types.StateAwaitingInput
		}
	}
}

// handleFinishAction 处理任务正常结束。
// 重要：FinishAction 即使是需要观察结果的类型也不挂起——结束是终态，不需要观察结果闭环。
func (c *Controller) handleFinishAction(action *types.FinishAction) {
	if action.GetBase().Source != types.SourceAgent {
		return
	}
	c.state.PendingAction = nil
	c.state.AgentState = types.StateFinished
}

// handleObservation 是因果闭环的关键。
// 只有当观察结果的 Cause 精确匹配当前 PendingAction 的 ID 时，才解锁。
// 这防止了：旧 observation 回来后误解除新的 PendingAction。
func (c *Controller) handleObservation(obs types.Observation) {
	if c.state.PendingAction == nil {
		return
	}
	if obs.GetBase().Cause == c.state.PendingAction.GetBase().ID {
		c.state.PendingAction = nil
	}
}

// shouldStep 决定收到某个事件后是否推进 agent。
// 设计原则：
//   - Observation 总是触发（因果闭环后自然需要继续推理）。
//   - 用户消息在等待态/初始态触发（给系统一个重启的机会）。
//   - 其他所有情况不触发，避免 agent 在任意事件刺激下无意义地重复推理。
func (c *Controller) shouldStep(evt types.Event) bool {
	if c.state.AgentState == types.StateFinished || c.state.AgentState == types.StateError {
		return false
	}
	if evt.Kind() == types.KindObservation {
		return true
	}
	action, ok := evt.(*types.MessageAction)
	return ok && action.GetBase().Source == types.SourceUser
}

// requiresObservation 定义哪些 action 类型需要等待环境回执。
// 当前只有 CmdRunAction 需要，其他 action 类型都是"即时完成"。
// 如果后续增加需要执行的工具（如 FileEdit），也需要在这里注册。
func requiresObservation(action types.Action) bool {
	switch action.(type) {
	case *types.CmdRunAction:
		return true
	default:
		return false
	}
}
