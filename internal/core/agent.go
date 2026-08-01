package core

import (
	"context"

	"github.com/wen/opentalon/internal/types"
)

// Agent 定义会话执行时的智能体行为接口。
type Agent interface {
	// StreamStep 执行一次推理流程，并通过回调输出增量语义结果。
	StreamStep(ctx context.Context, state *SessionState, onOutput func(types.AgentOutput)) (*types.AgentTurnResult, error)
}
