package types

import (
	"sync"
)

type EventLog struct {
	mu     sync.RWMutex
	events []Event
}

func NewEventLog() *EventLog {
	return &EventLog{
		events: make([]Event, 0, 10),
	}
}

func (m *EventLog) Append(event Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
}

func (m *EventLog) GetEvents() []Event {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.events
}
