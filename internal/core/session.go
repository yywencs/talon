package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/wen/opentalon/internal/tool"
	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/logger"
)

// Session 是系统运行时的编排核心，驱动 Agent、ToolRouter 和事件回调链协同工作。
// 它管理会话循环、迭代上限和取消，并对外暴露 SubmitUserMessage/Run 等入口。
type Session struct {
	state        *SessionState    // 状态机（包含 Status）
	agent        Agent            // 大脑
	toolRouter   *ToolRouter      // 工具执行路由
	eventFactory *EventFactory    // 事件工厂
	on_event     *Callbacks       // on_event 回调链
	onStream     *StreamCallbacks // 流式展示
}

const defaultActionParallelism = 10

// NewSession 初始化会话运行时，创建状态机、工具路由器和事件工厂。
// 需要存两份 agent：Session 中的运行时实例用于实际执行 step()，
// State 中的持久化配置用于恢复对话状态。
func NewSession(agent Agent, on_event *Callbacks, persistenceDir string) *Session {
	if on_event == nil {
		on_event = NewCallbacks()
	}

	sessionState := NewSessionState(agent, persistenceDir)

	s := &Session{
		state:        sessionState,
		agent:        agent,
		toolRouter:   NewToolRouter(),
		eventFactory: NewEventFactory(),
		on_event:     on_event,
		onStream:     NewStreamCallbacks(),
	}

	logger.Info("会话运行时已初始化",
		"session_id", sessionState.ID,
		"persistence_dir", persistenceDir,
		"agent_type", fmt.Sprintf("%T", agent),
	)

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

// SubmitUserMessage 将用户输入写入事件历史，并将会话状态切回 Running。
func (s *Session) SubmitUserMessage(text string) error {
	if s == nil || s.state == nil || s.eventFactory == nil {
		return fmt.Errorf("session is not initialized")
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("user message is empty")
	}

	s.state.Status = StatusRunning
	event := s.eventFactory.NewMessageEvent(types.Message{
		Role: types.RoleUser,
		Content: []types.Content{
			types.TextContent{Text: text},
		},
	}, types.SourceUser)
	s.emit(event)
	return nil
}

// Run 是会话主循环：反复调用 Agent 推理、分发动作、接收观察，直到终态。
func (s *Session) Run(ctx context.Context) (runErr error) {
	ctx, runTrace := startSessionRunTrace(ctx, s)
	defer runTrace.Close(ctx, &runErr)

	for {
		if ctxErr := runTrace.FailContext(ctx, StatusStuck); ctxErr != nil {
			runErr = ctxErr
			return runErr
		}

		if s.state.Status == StatusPaused || s.state.Status == StatusStuck {
			break
		}

		s.state.IterationCount++
		if s.state.IterationCount >= s.state.MaxIterations {
			s.state.Status = StatusStuck
			logger.InfoWithCtx(ctx, "会话运行超最大迭代次数，已停止",
				"session_id", s.state.ID,
			)
		}

		if isTerminalStatus(s.state.Status) {
			break
		}

		result, err := s.agent.StreamStep(ctx, s.state, s.handleAgentOutput)
		if err != nil {
			// Even on error, the model may have consumed tokens; record if available.
			if result != nil {
				s.state.TokenTracker.Record(result.TokenUsage)
			}
			if ctxErr := runTrace.FailContext(ctx, StatusStuck); ctxErr != nil {
				runErr = ctxErr
				return runErr
			}
			runErr = runTrace.Fail(err)
			return runErr
		}
		if result == nil {
			nilResultErr := fmt.Errorf("session run: agent returned nil turn result")
			runErr = runTrace.FailInvalidResponse(nilResultErr)
			return runErr
		}
		s.state.TokenTracker.Record(result.TokenUsage)
		if result.Message != nil {
			s.emit(s.eventFactory.NewMessageEvent(*result.Message, types.SourceAgent))
		}

		actionEvents, err := s.eventFactory.BuildActionEvents(result.ToolCalls, types.SourceAgent, result.ActionReasoningContent)
		if err != nil {
			wrappedErr := fmt.Errorf("session run: build action events failed: %w", err)
			runErr = runTrace.Fail(wrappedErr)
			return runErr
		}
		actionEvents = hasFinishAction(actionEvents)
		if len(actionEvents) == 0 {
			if result.Finished {
				s.state.Status = StatusFinished
				continue
			}
			err := fmt.Errorf("session run: agent returned no actions and did not finish")
			runErr = runTrace.FailInvalidResponse(err)
			return runErr
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
	if s != nil && s.state != nil {
		ctx = tool.ContextWithSessionID(ctx, s.state.ID)
	}
	results := s.toolRouter.ExecuteBatch(ctx, actionEvents, defaultActionParallelism)
	for _, observationEvent := range results {
		if observationEvent == nil {
			continue
		}
		s.emit(observationEvent)
	}
}

// handleAgentOutput 是 Agent 流式回调的接收端，将增量逐 token 推送到 onStream。
func (s *Session) handleAgentOutput(output types.AgentOutput) {
	switch output.Kind {
	case types.AgentOutputMessageDelta:
		if output.TextDelta == "" {
			return
		}
		s.streamTextDelta(output.TextDelta)
	}
}

// emit 将事件写入历史、推进状态机、并通知外部回调链。
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

// applyEvent 根据事件更新会话状态机（如 finish 动作将状态切为 Finished）。
func (s *Session) applyEvent(event types.Event) {
	actionEvent, ok := event.(*types.ActionEvent)
	if !ok || actionEvent == nil {
		return
	}
	if actionEvent.ActionType == types.ActionFinish {
		s.state.Status = StatusFinished
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

func isTerminalStatus(status ExecutionStatus) bool {
	return status == StatusFinished || status == StatusStuck
}

// streamTextDelta 将文本增量推送到流式回调链。
func (s *Session) streamTextDelta(text string) {
	if text == "" {
		return
	}
	if s.onStream != nil {
		s.onStream.HandleTextDelta(text)
	}
}

// SetContextWindowLimit 设置模型上下文窗口的 token 上限，0 表示未知。
func (s *Session) SetContextWindowLimit(limit int) {
	if s == nil || s.state == nil || s.state.TokenTracker == nil {
		return
	}
	s.state.TokenTracker.SetContextWindowLimit(limit)
	logger.Info("上下文窗口限制已设置",
		"session_id", s.state.ID,
		"context_window_tokens", limit,
	)
}
