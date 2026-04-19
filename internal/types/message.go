package types

type MessageEvent struct {
	BaseEvent
	Source        EventSource `json:"event_source"`
	LLMMessage    Message     `json:"message"`
	LLMResponseID string      `json:"llm_response_id,omitempty"`
}

func (e *MessageEvent) GetBase() *BaseEvent { return &e.BaseEvent }
func (e *MessageEvent) Kind() EventKind     { return KindMessage }
func (e *MessageEvent) Name() string        { return "message" }

func (e *MessageEvent) ToMessage() Message {
	src := e.LLMMessage
	dst := Message{
		Role:             src.Role,
		ToolCallID:       src.ToolCallID,
		Name:             src.Name,
		ReasoningContent: src.ReasoningContent,
	}

	if src.Content != nil {
		dst.Content = make([]Content, len(src.Content))
		for i, c := range src.Content {
			switch v := c.(type) {
			case TextContent:
				dst.Content[i] = TextContent{
					BaseContent: BaseContent{CachePrompt: v.CachePrompt},
					Text:        v.Text,
				}
			case *TextContent:
				if v != nil {
					dst.Content[i] = &TextContent{
						BaseContent: BaseContent{CachePrompt: v.CachePrompt},
						Text:        v.Text,
					}
				}
			case ImageContent:
				urls := make([]string, len(v.ImageURLs))
				copy(urls, v.ImageURLs)
				dst.Content[i] = ImageContent{
					BaseContent: BaseContent{CachePrompt: v.CachePrompt},
					ImageURLs:   urls,
				}
			case *ImageContent:
				if v != nil {
					urls := make([]string, len(v.ImageURLs))
					copy(urls, v.ImageURLs)
					dst.Content[i] = &ImageContent{
						BaseContent: BaseContent{CachePrompt: v.CachePrompt},
						ImageURLs:   urls,
					}
				}
			default:
				dst.Content[i] = c
			}
		}
	}

	if src.ToolCalls != nil {
		dst.ToolCalls = make([]MessageToolCall, len(src.ToolCalls))
		copy(dst.ToolCalls, src.ToolCalls)
	}
	if src.ThinkingBlocks != nil {
		dst.ThinkingBlocks = make([]ThinkingBlock, len(src.ThinkingBlocks))
		copy(dst.ThinkingBlocks, src.ThinkingBlocks)
	}
	if src.RedactedThinkingBlocks != nil {
		dst.RedactedThinkingBlocks = make([]RedactedThinkingBlock, len(src.RedactedThinkingBlocks))
		copy(dst.RedactedThinkingBlocks, src.RedactedThinkingBlocks)
	}
	if src.ResponsesReasoningItem != nil {
		newItem := &ReasoningItemModel{
			ID:               src.ResponsesReasoningItem.ID,
			EncryptedContent: src.ResponsesReasoningItem.EncryptedContent,
			Status:           src.ResponsesReasoningItem.Status,
		}

		if dst.ResponsesReasoningItem.Summary != nil {
			newItem.Summary = make([]string, len(src.ResponsesReasoningItem.Summary))
			copy(newItem.Summary, src.ResponsesReasoningItem.Summary)
		}
		if src.ResponsesReasoningItem.Content != nil {
			newItem.Content = make([]string, len(src.ResponsesReasoningItem.Content))
			copy(newItem.Content, src.ResponsesReasoningItem.Content)
		}

		dst.ResponsesReasoningItem = newItem
	}

	return dst
}
