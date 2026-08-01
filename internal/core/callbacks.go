package core

import (
	"sync"

	"github.com/wen/opentalon/internal/types"
)

// Callbacks 是事件回调链，在事件经过 Session 后按注册顺序逐个调用。
// 用于将事件分发到外部消费者（如 CLI 渲染器）。
type Callbacks struct {
	callbacks []func(e types.Event)
	mu        sync.RWMutex
}

// NewCallbacks 创建空的事件回调链。
func NewCallbacks() *Callbacks {
	return &Callbacks{
		callbacks: make([]func(e types.Event), 0),
	}
}

// Add 向回调链注册一个或多个事件处理器。
func (h *Callbacks) Add(callbacks ...func(e types.Event)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.callbacks = append(h.callbacks, callbacks...)
}

// Handle 按注册顺序逐个调用回调处理器。
func (h *Callbacks) Handle(e types.Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, cb := range h.callbacks {
		cb(e)
	}
}

// StreamCallbacks 是流式增量回调链，处理 Agent 推理过程中逐 token 推送的文字。
type StreamCallbacks struct {
	onTextDelta []func(text string)
}

// NewStreamCallbacks 创建空的流式回调链。
func NewStreamCallbacks() *StreamCallbacks {
	return &StreamCallbacks{
		onTextDelta: make([]func(text string), 0),
	}
}

// AddTextDelta 注册文本增量回调。
func (h *StreamCallbacks) AddTextDelta(callbacks ...func(text string)) {
	h.onTextDelta = append(h.onTextDelta, callbacks...)
}

// HandleTextDelta 将文本增量逐个推送到已注册的回调。
func (h *StreamCallbacks) HandleTextDelta(text string) {
	for _, cb := range h.onTextDelta {
		cb(text)
	}
}
