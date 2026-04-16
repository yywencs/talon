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
	"fmt"
	"strings"

	toolpkg "github.com/wen/opentalon/internal/tool"
	"github.com/wen/opentalon/internal/types"
)

type PromptBuilder struct{}

func NewPromptBuilder() *PromptBuilder {
	return &PromptBuilder{}
}

func (b *PromptBuilder) BuildMessages(state *types.State, systemPrompt string, userPromptExamples string) []ChatMessage {
	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
	}

	if userPromptExamples != "" {
		messages = append(messages, ChatMessage{
			Role:    "user",
			Content: userPromptExamples,
		})
	}

	for _, evt := range state.History {
		switch e := evt.(type) {
		case *types.MessageAction:
			messages = append(messages, ChatMessage{
				Role:    roleForSource(e.GetBase().Source),
				Content: e.Content,
			})
		case *types.CmdRunAction:
			content := fmt.Sprintf("Run command: %s", e.Command)
			if e.Thought != "" {
				content = fmt.Sprintf("%s\nReason: %s", content, e.Thought)
			}
			messages = append(messages, ChatMessage{
				Role:    roleForSource(e.GetBase().Source),
				Content: content,
			})
		case *types.ObservationEvent:
			if cmdObs, ok := e.Observation.(*toolpkg.TerminalObservation); ok {
				messages = append(messages, ChatMessage{
					Role: "user",
					Content: fmt.Sprintf(
						"Command result for action %s (exit_code=%d):\n%s",
						e.ActionID,
						cmdObs.ExitCodeValue(),
						cmdObs.OutputText(),
					),
				})
				break
			}

			text := types.FlattenTextContent(e.Observation.GetContent())
			if text != "" {
				messages = append(messages, ChatMessage{
					Role:    "user",
					Content: text,
				})
			}
		case *types.FinishAction:
			messages = append(messages, ChatMessage{
				Role:    "assistant",
				Content: "Finished task: " + e.Result,
			})
		default:
			if msg := strings.TrimSpace(evt.GetBase().Message); msg != "" {
				messages = append(messages, ChatMessage{
					Role:    roleForSource(evt.GetBase().Source),
					Content: msg,
				})
			}
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
func applyEphemeralCacheControls(messages []ChatMessage, hasExamples bool) {
	markerCount := 0

	addMarker := func(idx int) {
		if idx < 0 || idx >= len(messages) || markerCount >= maxExplicitCacheMarkers {
			return
		}
		if messages[idx].CacheControl != nil {
			return
		}

		messages[idx].CacheControl = map[string]string{
			"type": "ephemeral",
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

func roleForSource(source types.EventSource) string {
	if source == types.SourceUser {
		return "user"
	}
	return "assistant"
}
