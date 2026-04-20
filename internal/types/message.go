package types

type MessageEvent struct {
	BaseEvent
	Source EventSource `json:"event_source"`

	Role    MessageRole `json:"role"`
	Content []Content   `json:"content"`

	ReasoningContent string `json:"reasoning_content,omitempty"`
	LLMResponseID    string `json:"llm_response_id,omitempty"`
}

func (e *MessageEvent) GetBase() *BaseEvent { return &e.BaseEvent }
func (e *MessageEvent) Kind() EventKind     { return KindMessage }
func (e *MessageEvent) Name() string        { return "message" }

func (e *MessageEvent) ToMessage() Message {
	if e == nil {
		return Message{}
	}

	msg := Message{
		Role:             e.Role,
		ReasoningContent: e.ReasoningContent,
	}

	if e.Content != nil {
		msg.Content = make([]Content, len(e.Content))
		for i, c := range e.Content {
			switch v := c.(type) {
			case TextContent:
				msg.Content[i] = TextContent{
					BaseContent: BaseContent{CachePrompt: v.CachePrompt},
					Text:        v.Text,
				}
			case *TextContent:
				if v != nil {
					msg.Content[i] = &TextContent{
						BaseContent: BaseContent{CachePrompt: v.CachePrompt},
						Text:        v.Text,
					}
				}
			case ImageContent:
				urls := make([]string, len(v.ImageURLs))
				copy(urls, v.ImageURLs)
				msg.Content[i] = ImageContent{
					BaseContent: BaseContent{CachePrompt: v.CachePrompt},
					ImageURLs:   urls,
				}
			case *ImageContent:
				if v != nil {
					urls := make([]string, len(v.ImageURLs))
					copy(urls, v.ImageURLs)
					msg.Content[i] = &ImageContent{
						BaseContent: BaseContent{CachePrompt: v.CachePrompt},
						ImageURLs:   urls,
					}
				}
			default:
				msg.Content[i] = c
			}
		}
	}

	return msg
}
