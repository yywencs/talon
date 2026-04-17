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

func (b *PromptBuilder) BuildMessages(state *types.State, systemPrompt string, userPromptExamples string) []types.Message {
	return b.BuildPromptMessages(state, systemPrompt, userPromptExamples)
}

func (b *PromptBuilder) BuildPromptMessages(state *types.State, systemPrompt string, userPromptExamples string) []types.Message {
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

	for _, evt := range state.History {
		switch e := evt.(type) {
		case *types.MessageAction:
			messages = append(messages, types.Message{
				Role: roleForSourceMessage(e.GetSource()),
				Content: []types.Content{
					types.TextContent{Text: e.Content},
				},
			})
		case *types.ActionEvent:
			terminalAction, ok := e.Action.(*toolpkg.TerminalAction)
			if !ok {
				break
			}
			messages = append(messages, types.Message{
				Role: roleForSourceMessage(e.GetSource()),
				Content: []types.Content{
					types.TextContent{Text: fmt.Sprintf("Run command: %s", terminalAction.Command)},
				},
			})
		case *types.ObservationEvent:
			if cmdObs, ok := e.Observation.(*toolpkg.TerminalObservation); ok {
				messages = append(messages, types.Message{
					Role: types.RoleUser,
					Content: []types.Content{
						types.TextContent{
							Text: fmt.Sprintf(
								"Command result for action %s (exit_code=%d):\n%s",
								e.ActionID,
								cmdObs.ExitCodeValue(),
								cmdObs.OutputText(),
							),
						},
					},
				})
				break
			}

			text := types.FlattenTextContent(e.Observation.GetContent())
			if text != "" {
				messages = append(messages, types.Message{
					Role: types.RoleUser,
					Content: []types.Content{
						types.TextContent{Text: text},
					},
				})
			}
		case *types.FinishAction:
			messages = append(messages, types.Message{
				Role: types.RoleAssistant,
				Content: []types.Content{
					types.TextContent{Text: "Finished task: " + e.Result},
				},
			})
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

func roleForSource(source types.EventSource) string {
	if source == types.SourceUser {
		return "user"
	}
	return "assistant"
}

func roleForSourceMessage(source types.EventSource) types.MessageRole {
	if source == types.SourceUser {
		return types.RoleUser
	}
	return types.RoleAssistant
}

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
