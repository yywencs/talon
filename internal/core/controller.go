package core

import (
	"context"

	"github.com/wen/opentalon/internal/agent"
	toolpkg "github.com/wen/opentalon/internal/tool"
	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/logger"
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
// 顺序：处理事件副作用 → 判断是否推进 agent。
func (c *Controller) OnEvent(evt types.Event) {
	switch e := evt.(type) {
	case *types.ActionEvent:
		c.handleActionEvent(e)
	case types.Action:
		c.handleDirectAction(e)
	case *types.ObservationEvent:
		c.handleObservation(e)
	}

	logger.Debug("当前状态为",
		"evtKind", evt.Kind(),
		"evtType", evt.Name(),
		"evtID", evt.GetID(),
		"state", c.state.AgentState,
		"source", evt.GetSource())

	c.state.History = c.bus.History()

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

	action, err := c.agent.Step(context.Background(), c.state)

	if err != nil {
		logger.Error("agent.Step失败", "error", err)
		c.state.LastError = err.Error()
		c.setAgentStateTo(types.StateError)
		return
	}
	if action == nil {
		logger.Error("agent.Step返回nil action")
		return
	}

	if eventAction, ok := action.(interface {
		types.Event
		GetBase() *types.BaseEvent
	}); ok {
		eventAction.GetBase().Source = types.SourceAgent
		c.bus.Publish(eventAction)
		return
	}

	c.bus.Publish(&types.ActionEvent{
		BaseEvent: types.BaseEvent{
			Source: types.SourceAgent,
		},
		ActionType: action.ActionType(),
		Action:     action,
	})
}

// handleAction 根据 action 类型驱动状态机。
// 关键语义：Source 决定这条消息是"用户说的"还是"agent 说的"，
// 两者在状态转换中承担完全不同的角色。
func (c *Controller) handleDirectAction(action types.Action) {
	switch a := action.(type) {
	case *types.MessageAction:
		if a.GetBase().Source == types.SourceUser {
			logger.Info("👤 用户输入", "content", a.Content)
		} else {
			logger.Info("🤖 Agent回复", "content", a.Content)
		}
		c.handleMessageAction(a)
	case *types.FinishAction:
		if a.GetBase().Source == types.SourceAgent {
			logger.Info("✅ 任务完成", "result", a.Result)
		}
		c.handleFinishAction(a)
	}
}

func (c *Controller) handleActionEvent(evt *types.ActionEvent) {
	if evt == nil || evt.Action == nil {
		return
	}

	switch a := evt.Action.(type) {
	case *toolpkg.TerminalAction:
		logger.Info("⚡ Agent执行命令", "command", a.Command)
		c.handleTerminalAction(evt)
	}
}

// handleMessageAction 处理用户消息和 agent 消息在状态转换上的差异。
// 用户消息：打破等待态/初始态，将状态切回运行。
// agent 消息：WaitForResponse=true 表示需要人工确认，切到AwaitingInput 态暂停循环。
func (c *Controller) handleMessageAction(action *types.MessageAction) {
	source := action.GetBase().Source

	switch source {
	case types.SourceUser:
		if c.state.AgentState == types.StateLoading || c.state.AgentState == types.StateAwaitingInput {
			c.setAgentStateTo(types.StateRunning)
		}
	case types.SourceAgent:
		if action.WaitForResponse {
			c.setAgentStateTo(types.StateAwaitingInput)
		}
	}
}

// handleFinishAction 处理任务正常结束。
// 重要：FinishAction 即使是需要观察结果的类型也不挂起——结束是终态，不需要观察结果闭环。
// 但在设置终态前必须清除 PendingAction，防止遗留的未完成命令阻塞后续会话。
func (c *Controller) handleFinishAction(action *types.FinishAction) {
	if action.GetBase().Source != types.SourceAgent {
		return
	}
	c.state.PendingAction = nil
	c.setAgentStateTo(types.StateFinished)
}

// handleObservation 是因果闭环的关键。
// 只有当观察结果的 Cause 精确匹配当前 PendingAction 的 ID 时，才解锁。
// 这防止了：旧 observation 回来后误解除新的 PendingAction。
func (c *Controller) handleObservation(evt *types.ObservationEvent) {
	if c.state.PendingAction == nil {
		return
	}

	if evt == nil || evt.Observation == nil {
		return
	}

	if evt.ActionID == c.state.PendingAction.GetID() {
		content := truncate(types.FlattenTextContent(evt.Observation.GetContent()), 100)
		logger.Info("📋 命令执行结果", "content", content)
		c.state.PendingAction = nil
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func (c *Controller) setAgentStateTo(newState types.AgentState) {
	logger.Debug("状态变化",
		"from", c.state.AgentState,
		"to", newState)
	c.state.AgentState = newState
}

// handleCmdRunAction 处理命令执行动作
func (c *Controller) handleTerminalAction(evt *types.ActionEvent) {
	if evt.GetBase().Source == types.SourceAgent {
		c.state.PendingAction = evt
	}
}

// shouldStep 决定收到某个事件后是否推进 agent。
// 设计原则：
//   - Observation 总是触发（因果闭环后自然需要继续推理）。
//   - 用户消息在等待态/初始态触发（给系统一个重启的机会）。
//   - 其他所有情况不触发，避免 agent 在任意事件刺激下无意义地重复推理。
func (c *Controller) shouldStep(evt types.Event) bool {
	evtKind := evt.Kind()

	if c.state.AgentState == types.StateFinished {
		return false
	}
	if c.state.AgentState == types.StateError {
		return false
	}
	if c.state.PendingAction != nil {
		return false
	}

	if evtKind == types.KindObservation {
		return true
	}

	action, ok := evt.(*types.MessageAction)
	if !ok {
		return false
	}

	source := action.GetBase().Source
	if source == types.SourceUser {
		return true
	}

	if action.WaitForResponse {
		return false
	}

	return false
}
