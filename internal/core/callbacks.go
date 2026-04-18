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
