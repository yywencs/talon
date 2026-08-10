package simulator

import (
	"context"
	"fmt"
	"time"

	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/scenario"
)

var _ platform.ToolOpsPlatform = (*Simulator)(nil)

// Simulator 使用有状态World实现与真实平台相同的ToolOpsPlatform契约。
type Simulator struct {
	world *World
}

// New 根据Scenario创建独立的Simulator实例。
func New(document scenario.Scenario) (*Simulator, error) {
	world, err := NewWorld(document)
	if err != nil {
		return nil, err
	}
	return &Simulator{world: world}, nil
}

// Snapshot 返回当前虚拟世界的只读副本。
func (s *Simulator) Snapshot() Snapshot {
	if s == nil || s.world == nil {
		return Snapshot{}
	}
	return s.world.Snapshot()
}

// Advance 推进虚拟时间，并按时间顺序应用Timeline事件和到期Operation。
func (s *Simulator) Advance(ctx context.Context, duration time.Duration) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil || s.world == nil {
		return fmt.Errorf("simulator world is not initialized")
	}
	if duration < 0 {
		return fmt.Errorf("advance duration must not be negative")
	}
	w := s.world
	w.mu.Lock()
	defer w.mu.Unlock()
	target := w.now.Add(duration)
	if target.After(w.endAt) {
		return fmt.Errorf("advance target %s exceeds world end time %s", target, w.endAt)
	}
	return w.advanceToLocked(target)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
