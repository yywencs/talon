package core

import (
	"sync"

	"github.com/wen/opentalon/internal/types"
)

type Callbacks struct {
	callbacks []func(e types.Event)
	mu        sync.RWMutex
}

func NewCallbacks() *Callbacks {
	return &Callbacks{
		callbacks: make([]func(e types.Event), 0),
	}
}

func (h *Callbacks) Add(callbacks ...func(e types.Event)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.callbacks = append(h.callbacks, callbacks...)
}

func (h *Callbacks) Handle(e types.Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, cb := range h.callbacks {
		cb(e)
	}
}

type StreamCallbacks struct {
	onTextDelta []func(text string)
}

func NewStreamCallbacks() *StreamCallbacks {
	return &StreamCallbacks{
		onTextDelta: make([]func(text string), 0),
	}
}

func (h *StreamCallbacks) AddTextDelta(callbacks ...func(text string)) {
	h.onTextDelta = append(h.onTextDelta, callbacks...)
}

func (h *StreamCallbacks) HandleTextDelta(text string) {
	for _, cb := range h.onTextDelta {
		cb(text)
	}
}
