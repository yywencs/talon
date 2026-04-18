package types

import "context"

// Agent 定义会话执行时的智能体行为接口。
type Agent interface {
	// Step 执行一次推理与动作流程，并通过 onEvent 实时推送产出的事件。
	Step(ctx context.Context, state *SessionState, onEvent func(Event)) error
}
