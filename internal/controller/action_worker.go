package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/wen/opentalon/internal/execution"
	"github.com/wen/opentalon/internal/workflow"
)

// ActionWorker 持续推进一个 IncidentWorkflow 的异步修复 Operation。
// 执行进度保存在 execution.Store 中，因此 Worker 重启后可以继续轮询原 Operation。
type ActionWorker struct {
	processor     *PlanProcessor
	retryInterval time.Duration
}

// NewActionWorker 创建异步 Action 调度器。
func NewActionWorker(processor *PlanProcessor, retryInterval time.Duration) (*ActionWorker, error) {
	if processor == nil || processor.executionStore == nil || processor.workflow == nil {
		return nil, fmt.Errorf("initialized plan processor is required")
	}
	if retryInterval <= 0 {
		return nil, fmt.Errorf("worker retry interval must be positive")
	}
	return &ActionWorker{processor: processor, retryInterval: retryInterval}, nil
}

// Run 运行到修复阶段结束或 Context 被取消。
// pending/running/unknown 会按照数据库中的 NextPollAt 等待，不会在内存中高频空转。
func (w *ActionWorker) Run(ctx context.Context) error {
	if w == nil || w.processor == nil {
		return fmt.Errorf("action worker is not initialized")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if w.processor.workflow.Snapshot().State != workflow.StateRemediating {
			return nil
		}

		record, err := w.processor.ExecuteNext(ctx)
		if err != nil && !errors.Is(err, execution.ErrNoClaimable) && !errors.Is(err, ErrActionExecutionUnknown) {
			return err
		}
		if w.processor.workflow.Snapshot().State != workflow.StateRemediating {
			return err
		}
		delay := w.nextDelay(ctx, record)
		if err := waitContext(ctx, delay); err != nil {
			return err
		}
	}
}

func (w *ActionWorker) nextDelay(ctx context.Context, current execution.Record) time.Duration {
	now := time.Now().UTC()
	if current.NextPollAt != nil {
		return nonNegative(current.NextPollAt.Sub(now))
	}
	snapshot := w.processor.workflow.Snapshot()
	if snapshot.Plan == nil {
		return w.retryInterval
	}
	records, err := w.processor.executionStore.ListPlan(ctx, snapshot.Plan.ID)
	if err != nil {
		return w.retryInterval
	}
	for _, record := range records {
		if record.Status == execution.StatusSucceeded || record.Status == execution.StatusFailed {
			continue
		}
		if record.NextPollAt != nil {
			return nonNegative(record.NextPollAt.Sub(now))
		}
		if record.LeaseUntil != nil && record.LeaseUntil.After(now) {
			return nonNegative(record.LeaseUntil.Sub(now))
		}
		return 0
	}
	return w.retryInterval
}

func nonNegative(value time.Duration) time.Duration {
	if value < 0 {
		return 0
	}
	return value
}

func waitContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
