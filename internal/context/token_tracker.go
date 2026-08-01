// Package context 承载会话上下文管理相关的运行时行为，
// 包括 token 预算追踪、上下文估算和未来的压缩策略。
package context

import (
	"sync"

	"github.com/wen/opentalon/internal/types"
)

// TokenTracker 在会话内逐轮累计 token 用量。
//
// 设计参考 Codex context-governance 的 token_budget_context：
//   - lastUsage 保存最近一次服务端上报的 token 用量，
//     反映最后一次 API 调用时活跃上下文的大小。
//   - totalUsage 跨轮累计，用于成本追踪。
//   - contextWindow 为模型上下文窗口的 token 上限；
//     0 表示未设置/未知。
//
// 所有方法均并发安全。
type TokenTracker struct {
	mu sync.RWMutex

	lastUsage     types.TokenUsage
	totalUsage    types.TokenUsage
	contextWindow int
}

// NewTokenTracker 创建一个空的 tracker。
func NewTokenTracker() *TokenTracker {
	return &TokenTracker{}
}

// Record 记录单次模型调用的服务端上报 token 用量。
// 会替换 lastUsage 快照，并累加到 totalUsage。
func (t *TokenTracker) Record(usage types.TokenUsage) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastUsage = usage
	t.totalUsage.PromptTokens += usage.PromptTokens
	t.totalUsage.CompletionTokens += usage.CompletionTokens
}

// LastUsage 返回最近一次服务端上报的 token 用量。
func (t *TokenTracker) LastUsage() types.TokenUsage {
	if t == nil {
		return types.TokenUsage{}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.lastUsage
}

// TotalUsage 返回会话内累计的 token 用量。
func (t *TokenTracker) TotalUsage() types.TokenUsage {
	if t == nil {
		return types.TokenUsage{}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.totalUsage
}

// SetContextWindowLimit 设置模型上下文窗口的 token 上限。
// 值为 0 表示限制未知/未设置。
func (t *TokenTracker) SetContextWindowLimit(limit int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.contextWindow = limit
}

// ContextWindowLimit 返回已配置的上下文窗口 token 上限，
// 未设置时返回 0。
func (t *TokenTracker) ContextWindowLimit() int {
	if t == nil {
		return 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.contextWindow
}

// ActiveContextTokens 估算当前活跃上下文的 token 数量。
//
// 目前返回服务端最近一次 API 调用上报的 total_tokens。
// 后续接入客户端估算（追踪模型响应之后新增事件的 token 消耗）后，
// 将变为：lastUsage.Total() + 估算的增量 token 数。
//
// 当前阶段，仅使用服务端上报值是最简单且准确的信号。
func (t *TokenTracker) ActiveContextTokens() int {
	if t == nil {
		return 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.lastUsage.Total()
}
