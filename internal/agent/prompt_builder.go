// PromptBuilder 负责将 State.History（事件序列）转换为 LLM 可以理解的对话消息序列。
// 它处理两类任务：
//  1. 系统指令的注入（system prompt + 场景指令）。
//  2. 事件历史到对话消息的语义映射（event -> role/content）。
//
// 设计原则：
//   - History 中的每条事件都应被映射为明确的 role，不存在"无法归类的经验被丢弃"。
//   - system prompt 在每次 step() 调用时重新生成，而不是缓存在 State 中；
//     这样 agent 在每轮推理时都能拿到最新的指令文本。
package agent

import (
	"encoding/json"
	"strings"

	"github.com/wen/opentalon/internal/core"
	"github.com/wen/opentalon/internal/types"
)

// PromptBuilder 负责将 SessionState 中的事件历史转换为 LLM 可消费的对话消息序列。
type PromptBuilder struct{}

// NewPromptBuilder 创建空构建器。
func NewPromptBuilder() *PromptBuilder {
	return &PromptBuilder{}
}

// BuildMessages 构建完整的 LLM 消息序列（系统提示 + 示例 + 历史事件）。
func (b *PromptBuilder) BuildMessages(state *core.SessionState, systemPrompt string, userPromptExamples string) []types.Message {
	return b.BuildPromptMessages(state, systemPrompt, userPromptExamples)
}

// BuildPromptMessages 是实际构建逻辑：注入系统提示、用户示例、历史事件序列，并打缓存标记。
func (b *PromptBuilder) BuildPromptMessages(state *core.SessionState, systemPrompt string, userPromptExamples string) []types.Message {
	messages := []types.Message{
		{
			Role: types.RoleSystem,
			Content: []types.Content{
				types.TextContent{Text: systemPrompt},
			},
		},
	}

	if userPromptExamples != "" {
		messages = append(messages, types.Message{
			Role: types.RoleUser,
			Content: []types.Content{
				types.TextContent{Text: userPromptExamples},
			},
		})
	}

	if state.Events != nil {
		for _, evt := range state.Events.GetEvents() {
			msg, ok := eventToMessage(evt)
			if !ok {
				continue
			}
			messages = append(messages, msg)
		}
	}

	applyEphemeralCacheControls(messages, strings.TrimSpace(userPromptExamples) != "")
	return messages
}

const (
	maxExplicitCacheMarkers = 4
	cacheLookbackWindow     = 20
)

// applyEphemeralCacheControls 为稳定前缀和滚动上下文打缓存标记，
// 兼顾更高命中率与 provider 单次最多 4 个标记、单标记最多回溯 20 个内容块的限制。
// applyEphemeralCacheControls 为稳定前缀和滚动上下文打缓存标记，
// 兼顾更高命中率与 provider 单次最多 4 个标记、单标记最多回溯 20 个内容块的限制。
func applyEphemeralCacheControls(messages []types.Message, hasExamples bool) {
	markerCount := 0

	addMarker := func(idx int) {
		if idx < 0 || idx >= len(messages) || markerCount >= maxExplicitCacheMarkers {
			return
		}
		if len(messages[idx].Content) == 0 {
			return
		}
		if hasCachePrompt(messages[idx].Content) {
			return
		}
		switch content := messages[idx].Content[len(messages[idx].Content)-1].(type) {
		case types.TextContent:
			content.CachePrompt = true
			messages[idx].Content[len(messages[idx].Content)-1] = content
		case *types.TextContent:
			if content != nil {
				content.CachePrompt = true
			}
		case types.ImageContent:
			content.CachePrompt = true
			messages[idx].Content[len(messages[idx].Content)-1] = content
		case *types.ImageContent:
			if content != nil {
				content.CachePrompt = true
			}
		}
		markerCount++
	}

	addMarker(0)
	if hasExamples {
		addMarker(1)
	}
	for idx := len(messages) - 1; idx >= 0 && markerCount < maxExplicitCacheMarkers; idx -= cacheLookbackWindow {
		addMarker(idx)
	}
}

// hasCachePrompt 检查内容块中是否已存在 cache_prompt 标记。
func hasCachePrompt(contents []types.Content) bool {
	for _, content := range contents {
		switch c := content.(type) {
		case types.TextContent:
			if c.CachePrompt {
				return true
			}
		case *types.TextContent:
			if c != nil && c.CachePrompt {
				return true
			}
		case types.ImageContent:
			if c.CachePrompt {
				return true
			}
		case *types.ImageContent:
			if c != nil && c.CachePrompt {
				return true
			}
		}
	}
	return false
}

// eventToMessage 将单个事件映射为 LLM 对话消息（role + content）；
// 无法映射或空载荷的事件返回 (zero, false)。
func eventToMessage(evt types.Event) (types.Message, bool) {
	filter := func(msg types.Message) (types.Message, bool) {
		if msg.Role == "" {
			return types.Message{}, false
		}
		if len(msg.Content) == 0 &&
			len(msg.ToolCalls) == 0 &&
			msg.ReasoningContent == "" &&
			len(msg.ThinkingBlocks) == 0 &&
			len(msg.RedactedThinkingBlocks) == 0 &&
			msg.ResponsesReasoningItem == nil {
			return types.Message{}, false
		}
		return msg, true
	}

	switch evt.Kind() {
	case types.KindAction:
		if e, ok := evt.(*types.ActionEvent); ok {
			return filter(e.ToMessage())
		}
	case types.KindObservation:
		if e, ok := evt.(*types.ObservationEvent); ok {
			return filter(e.ToMessage())
		}
	case types.KindMessage:
		if e, ok := evt.(*types.MessageEvent); ok {
			return filter(e.ToMessage())
		}
	}
	return types.Message{}, false
}

// eventToJSON 将事件映射为消息后 JSON 序列化，用于调试/追踪。
func eventToJSON(evt types.Event) (string, bool) {
	msg, ok := eventToMessage(evt)
	if !ok {
		return "", false
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return "", false
	}
	return string(data), true
}
