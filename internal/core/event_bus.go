package core

import (
	"time"

	"github.com/wen/opentalon/internal/types"
)

// Handler 是事件总线的订阅回调，收到事件时由总线同步调用。
type Handler func(types.Event)

// EventBus 是发布-订阅总线，所有组件通过它交换事件。
// 设计要点：
//   - 总线不负责路由，所有订阅者收到相同事件（广播语义）。
//   - Publish 期间 ID 由总线统一分配，保证因果链可追踪。
//   - 不需要异步或并发写入保护，因为调用方（controller）是单 goroutine 驱动。
type EventBus struct {
	handlers []Handler
	nextID   int64
}

// Subscribe 将 handler 加入广播列表。调用时机在系统初始化阶段，不会在运行期频繁增删。
func (b *EventBus) Subscribe(h Handler) {
	b.handlers = append(b.handlers, h)
}

// NewEventBus 构造一个空的 EventBus。
func NewEventBus() *EventBus {
	return &EventBus{}
}

// Publish 将事件广播给所有订阅者，同时补全 ID 和时间戳。
// ID 分配策略：从 1 开始递增，遇到已有 ID（外部预分配）则跳过。
// 这样设计保证：只要事件进了总线，它的 ID 就在整个会话内唯一且稳定，
// 后续的 Cause 匹配不需要任何协调。
func (b *EventBus) Publish(e types.Event) {
	base := e.GetBase()
	if base.ID == 0 {
		b.nextID++
		base.ID = b.nextID
	}
	if base.Timestamp.IsZero() {
		base.Timestamp = time.Now()
	}

	for _, h := range b.handlers {
		h(e)
	}
}
