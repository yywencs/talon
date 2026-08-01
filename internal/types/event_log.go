package types

import (
	"sync"
)

// EventLog 是会话内事件的有序日志，提供并发安全的追加和快照读取。
type EventLog struct {
	mu     sync.RWMutex
	events []Event
}

// NewEventLog 创建一个空的事件日志。
func NewEventLog() *EventLog {
	return &EventLog{
		events: make([]Event, 0, 10),
	}
}

// Append 向日志末尾追加一条事件。
func (m *EventLog) Append(event Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
}

// GetEvents 返回日志中所有事件的快照副本，调用方可安全遍历。
func (m *EventLog) GetEvents() []Event {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Event{}, m.events...)
}
