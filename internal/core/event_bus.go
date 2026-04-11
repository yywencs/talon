package core

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/logger"
)

// Handler 是事件总线的订阅回调，收到事件时由总线调用。
type Handler func(types.Event)

// EventBus 是发布-订阅总线，所有组件通过它交换事件。
// 设计要点：
//   - 使用 channel 队列解耦生产者和消费者。
//   - Publish 将事件放入 channel 立即返回，不阻塞。
//   - Start() 启动 goroutine 从 channel 消费事件并分发给所有 handler。
//   - 自动维护 History 序列，记录所有已发布的事件。
type EventBus struct {
	handlers  []Handler
	history   []types.Event
	nextID    atomic.Int64
	eventCh   chan types.Event
	wg        sync.WaitGroup
	stopCh    chan struct{}
	historyMu sync.RWMutex
}

// NewEventBus 构造一个空的 EventBus。
func NewEventBus() *EventBus {
	return &EventBus{
		eventCh: make(chan types.Event, 100),
		stopCh:  make(chan struct{}),
	}
}

// Subscribe 将 handler 加入广播列表。调用时机在系统初始化阶段，不会在运行期频繁增删。
func (b *EventBus) Subscribe(h Handler) {
	b.handlers = append(b.handlers, h)
}

// Publish 将事件发送到 channel 队列，立即返回。
// ID 分配策略：从 1 开始递增，遇到已有 ID（外部预分配）则跳过。
func (b *EventBus) Publish(e types.Event) {
	base := e.GetBase()

	if base.ID == 0 {
		base.ID = b.nextID.Add(1)
	}

	if base.Timestamp.IsZero() {
		base.Timestamp = time.Now()
	}

	b.eventCh <- e
}

// Start 启动 goroutine 从 channel 消费事件并分发给所有 handler。
// 应在所有 Subscribe 调用之后调用，且只能调用一次。
func (b *EventBus) Start() {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		for {
			select {
			case e := <-b.eventCh:
				b.historyMu.Lock()
				b.history = append(b.history, e)
				b.historyMu.Unlock()

				logger.Debug("发布新事件",
					"evtKind", e.Kind(),
					"evtID", e.GetBase().ID,
					"source", e.GetBase().Source)

				for _, h := range b.handlers {
					go h(e)
				}
			case <-b.stopCh:
				return
			}
		}
	}()
}

// Stop 停止事件处理循环，等待所有事件被处理完毕后返回。
func (b *EventBus) Stop() {
	close(b.stopCh)
	b.wg.Wait()
}

// History 返回所有已发布事件的副本。
func (b *EventBus) History() []types.Event {
	b.historyMu.RLock()
	defer b.historyMu.RUnlock()
	result := make([]types.Event, len(b.history))
	copy(result, b.history)
	return result
}
