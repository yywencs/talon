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

	"github.com/wen/opentalon/internal/types"
)

type PromptBuilder struct{}

func NewPromptBuilder() *PromptBuilder {
	return &PromptBuilder{}
}

func (b *PromptBuilder) BuildMessages(state *types.State, systemPrompt string) []ChatMessage {
	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
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
		case *types.CmdOutputObservation:
			messages = append(messages, ChatMessage{
				Role: "user",
				Content: fmt.Sprintf(
					"Command result for action %d (exit_code=%d):\n%s",
					e.GetBase().Cause,
					e.ExitCode,
					e.Content,
				),
			})
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

	return messages
}

func roleForSource(source types.EventSource) string {
	if source == types.SourceUser {
		return "user"
	}
	return "assistant"
}
