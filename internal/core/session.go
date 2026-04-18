package core

import (
	"context"

	"github.com/wen/opentalon/internal/types"
)

// Session 平替了你的 Conversation
type Session struct {
	state    *types.SessionState // 状态机（包含 Status）
	agent    types.Agent         // 大脑
	on_event *Callbacks          // on_event 回调链
}

// NewSession 初始化
// 需要存两份agent，seesion中的agent运行时实例，实际执行 step(), state中的agent持久化配置，用于恢复对话状态
func NewSession(agent types.Agent, on_event *Callbacks, persistenceDir string) *Session {

	sessionState := types.NewSessionState(agent, persistenceDir)

	s := &Session{
		state:    sessionState,
		agent:    agent,
		on_event: on_event,
	}

	s.on_event.Add(sessionState.Events.Append)

	return s
}

func (s *Session) Run(ctx context.Context) error {
	for {
		if s.state.Status == types.StatusPaused || s.state.Status == types.StatusStuck {
			break
		}

		if s.state.Status == types.StatusFinished {
			break
		}

		observation, err := s.agent.Step(ctx, s.state)
		if err != nil {
			return err
		}

		s.on_event.Handle(&observation)
	}
	return nil
}
