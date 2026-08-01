package types

// MessageEvent 是对话消息（用户输入或助手输出）的事件信封，
// 在事件总线上流过后被 PromptBuilder 映射回 LLM 对话历史。
type MessageEvent struct {
	BaseEvent
	Source EventSource `json:"event_source"`

	Role    MessageRole `json:"role"`
	Content []Content   `json:"content"`

	ReasoningContent string `json:"reasoning_content,omitempty"`
	LLMResponseID    string `json:"llm_response_id,omitempty"`
}

// GetBase 返回嵌套的事件信封指针。
func (e *MessageEvent) GetBase() *BaseEvent { return &e.BaseEvent }
// Kind 返回事件类型标签。
func (e *MessageEvent) Kind() EventKind     { return KindMessage }
// Name 返回固定的事件名称。
func (e *MessageEvent) Name() string        { return "message" }

// ToMessage 将 MessageEvent 转换为对应的 LLM 对话消息，
// 对 Content 做深拷贝以避免后续修改影响事件历史中的原始引用。
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
