package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/wen/opentalon/internal/types"
)

type Session struct {
	state        *types.SessionState // 状态机（包含 Status）
	agent        types.Agent         // 大脑
	toolRouter   *ToolRouter         // 工具执行路由
	eventFactory *EventFactory       // 事件工厂
	on_event     *Callbacks          // on_event 回调链
	onStream     *StreamCallbacks    // 流式展示
}

const defaultActionParallelism = 10

// NewSession 初始化
// 需要存两份agent，seesion中的agent运行时实例，实际执行 step(), state中的agent持久化配置，用于恢复对话状态
func NewSession(agent types.Agent, on_event *Callbacks, persistenceDir string) *Session {
	if on_event == nil {
		on_event = NewCallbacks()
	}

	sessionState := types.NewSessionState(agent, persistenceDir)

	s := &Session{
		state:        sessionState,
		agent:        agent,
		toolRouter:   NewToolRouter(),
		eventFactory: NewEventFactory(),
		on_event:     on_event,
		onStream:     NewStreamCallbacks(),
	}

	return s
}

// AddEventCallbacks 注册标准事件回调。
func (s *Session) AddEventCallbacks(callbacks ...func(types.Event)) {
	if s == nil {
		return
	}
	if s.on_event == nil {
		s.on_event = NewCallbacks()
	}
	s.on_event.Add(callbacks...)
}

// AddStreamTextDeltaCallbacks 注册流式文本增量回调。
func (s *Session) AddStreamTextDeltaCallbacks(callbacks ...func(string)) {
	if s == nil {
		return
	}
	if s.onStream == nil {
		s.onStream = NewStreamCallbacks()
	}
	s.onStream.AddTextDelta(callbacks...)
}

// SubmitUserMessage 将用户输入写入事件历史，并将会话切回可运行状态。
func (s *Session) SubmitUserMessage(text string) error {
	if s == nil || s.state == nil || s.eventFactory == nil {
		return fmt.Errorf("session is not initialized")
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("user message is empty")
	}

	s.state.Status = types.StatusRunning
	event := s.eventFactory.NewMessageEvent(types.Message{
		Role: types.RoleUser,
		Content: []types.Content{
			types.TextContent{Text: text},
		},
	}, types.SourceUser)
	s.emit(event)
	return nil
}

func (s *Session) Run(ctx context.Context) error {
	for {
		if s.state.Status == types.StatusPaused || s.state.Status == types.StatusStuck {
			break
		}

		if s.state.Status == types.StatusFinished {
			break
		}

		result, err := s.agent.StreamStep(ctx, s.state, s.handleAgentOutput)
		if err != nil {
			return err
		}
		if result == nil {
			return fmt.Errorf("session run: agent returned nil turn result")
		}
		if result.Message != nil {
			s.emit(s.eventFactory.NewMessageEvent(*result.Message, types.SourceAgent))
		}

		actionEvents, err := s.eventFactory.BuildActionEvents(result.ToolCalls, types.SourceAgent)
		if err != nil {
			return fmt.Errorf("session run: build action events failed: %w", err)
		}
		actionEvents = hasFinishAction(actionEvents)
		if len(actionEvents) == 0 {
			if result.Finished {
				s.state.Status = types.StatusFinished
				continue
			}
			return fmt.Errorf("session run: agent returned no actions and did not finish")
		}
		for _, actionEvent := range actionEvents {
			s.emit(actionEvent)
		}
		s.executeActionEvents(ctx, actionEvents)
	}
	return nil
}

// executeActionEvents 并行执行动作，并按输入顺序回调 ObservationEvent。
func (s *Session) executeActionEvents(ctx context.Context, actionEvents []*types.ActionEvent) {
	results := s.toolRouter.ExecuteBatch(ctx, actionEvents, defaultActionParallelism)
	for _, observationEvent := range results {
		if observationEvent == nil {
			continue
		}
		s.emit(observationEvent)
	}
}

func (s *Session) handleAgentOutput(output types.AgentOutput) {
	switch output.Kind {
	case types.AgentOutputMessageDelta:
		if output.TextDelta == "" {
			return
		}
		s.streamTextDelta(output.TextDelta)
	}
}

func (s *Session) emit(event types.Event) {
	if event == nil {
		return
	}
	s.applyEvent(event)
	if s.state != nil && s.state.Events != nil {
		s.state.Events.Append(event)
	}
	if s.on_event != nil {
		s.on_event.Handle(event)
	}
}

func (s *Session) applyEvent(event types.Event) {
	actionEvent, ok := event.(*types.ActionEvent)
	if !ok || actionEvent == nil {
		return
	}
	if actionEvent.ActionType == types.ActionFinish {
		s.state.Status = types.StatusFinished
	}
}

// hasFinishAction 截断 finish 动作之后的所有动作，只保留到 finish 动作本身。
// 如果不存在 finish 动作，则返回原始列表。
func hasFinishAction(actionEvents []*types.ActionEvent) []*types.ActionEvent {
	for i, e := range actionEvents {
		if e.ActionType == types.ActionFinish {
			return actionEvents[:i+1]
		}
	}
	return actionEvents
}

func (s *Session) streamTextDelta(text string) {
	if text == "" {
		return
	}
	if s.onStream != nil {
		s.onStream.HandleTextDelta(text)
	}
}
