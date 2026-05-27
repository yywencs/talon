package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/wen/opentalon/internal/tool"
	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/logger"
	"github.com/wen/opentalon/pkg/observability"
)

const (
	sessionTracerName  = "internal/core/session"
	sessionRunSpanName = "session.run"
)

// sessionRunTrace 封装 Session.Run 的 tracing 模板逻辑，避免主循环混入大量
// span 生命周期、错误归类、收尾和资源释放代码。
type sessionRunTrace struct {
	session *Session
	span    observability.Span
	cancel  context.CancelFunc
}

// startSessionRunTrace 初始化 session.run span，并在配置了 RunTimeout 时派生带超时的上下文。
func startSessionRunTrace(ctx context.Context, session *Session) (context.Context, *sessionRunTrace) {
	runTrace := &sessionRunTrace{session: session}
	if session != nil && session.state != nil && session.state.RunTimeout > 0 {
		ctx, runTrace.cancel = context.WithTimeout(ctx, session.state.RunTimeout)
	}

	ctx, runTrace.span = observability.TracerFor(sessionTracerName).StartSpan(ctx, sessionRunSpanName,
		observability.WithSpanKind(observability.SpanKindInternal),
		observability.WithAttributes(runTrace.initialAttributes()...),
	)

	return ctx, runTrace
}

// Close 统一处理 Session.Run 的 defer 收尾，包括 panic recover、最终 attributes、
// 资源释放、成功状态写回，以及 span 和 cancel 的生命周期结束。
func (t *sessionRunTrace) Close(ctx context.Context, runErr *error) {
	if t == nil {
		return
	}
	if t.cancel != nil {
		defer t.cancel()
	}
	if t.span != nil {
		defer t.span.End()
	}

	var recovered any
	if p := recover(); p != nil {
		recovered = p
		panicErr := fmt.Errorf("session run panic recovered: %v", p)
		if runErr != nil {
			*runErr = panicErr
		}
		t.fail(panicErr, types.StatusStuck, observability.SpanStatusPanicRecovered)
	}

	t.setFinalAttributes()
	t.releaseResources(ctx, runErr)
	if recovered != nil {
		panic(recovered)
	}
	if runErr == nil || *runErr != nil {
		return
	}
	t.span.SetStatus(observability.SpanStatusOK, t.successDescription())
}

// Fail 使用默认错误分类记录失败，不额外改写 session 状态。
func (t *sessionRunTrace) Fail(err error) error {
	return t.fail(err, "", observability.StatusFromError(err))
}

// FailContext 将上下文取消或超时转换为 session 运行错误，并在需要时写回 session 状态。
func (t *sessionRunTrace) FailContext(ctx context.Context, sessionStatus types.ExecutionStatus) error {
	err := sessionRunContextError(ctx)
	if err == nil {
		return nil
	}
	return t.fail(err, sessionStatus, observability.StatusFromError(err))
}

// FailInvalidResponse 记录模型返回非法响应这一类错误。
func (t *sessionRunTrace) FailInvalidResponse(err error) error {
	return t.fail(err, "", observability.SpanStatusLLMInvalidResponse)
}

// FailWithStatus 允许调用方显式指定 session 状态和 span 错误分类。
func (t *sessionRunTrace) FailWithStatus(err error, sessionStatus types.ExecutionStatus, spanStatus observability.SpanStatus) error {
	return t.fail(err, sessionStatus, spanStatus)
}

// fail 是统一失败模板：按需回写 session 状态，并在 span 上记录归类后的错误。
func (t *sessionRunTrace) fail(err error, sessionStatus types.ExecutionStatus, spanStatus observability.SpanStatus) error {
	if err == nil {
		return nil
	}
	if t != nil && t.session != nil && t.session.state != nil && sessionStatus != "" {
		t.session.state.Status = sessionStatus
	}
	if t != nil && t.span != nil {
		t.span.RecordError(err, spanStatus)
	}
	return err
}

// initialAttributes 构造 session.run span 启动时需要写入的基础属性。
func (t *sessionRunTrace) initialAttributes() []observability.Attribute {
	attrs := make([]observability.Attribute, 0, 4)
	if t == nil || t.session == nil || t.session.state == nil {
		return attrs
	}

	attrs = append(attrs,
		observability.String("session.id", t.session.state.ID),
		observability.String("session.status.initial", string(t.session.state.Status)),
	)
	if t.session.agent != nil {
		attrs = append(attrs, observability.String("agent.type", fmt.Sprintf("%T", t.session.agent)))
	}
	return attrs
}

// setFinalAttributes 在运行结束时补充最终状态和迭代次数等收尾属性。
func (t *sessionRunTrace) setFinalAttributes() {
	if t == nil || t.span == nil || t.session == nil || t.session.state == nil {
		return
	}
	t.span.SetAttributes(
		observability.String("session.status.final", string(t.session.state.Status)),
		observability.Int("session.iteration.count", t.session.state.IterationCount),
	)
}

// releaseResources 在会话结束或出错时释放会话级资源；对非终态的成功返回不做清理。
func (t *sessionRunTrace) releaseResources(ctx context.Context, runErr *error) {
	if t == nil || t.session == nil || t.session.state == nil {
		return
	}
	if runErr != nil && *runErr == nil && !isTerminalStatus(t.session.state.Status) {
		return
	}
	if err := tool.ReleaseBashSession(ctx, t.session.state.ID); err != nil {
		logger.WarnWithCtx(ctx, "会话结束时释放 bash 会话级 executor 失败",
			"session_id", t.session.state.ID,
			"error", err.Error(),
		)
	}
}

// successDescription 根据最终 session 状态生成成功路径的 span 描述。
func (t *sessionRunTrace) successDescription() string {
	if t == nil || t.session == nil || t.session.state == nil {
		return "session completed"
	}
	switch t.session.state.Status {
	case types.StatusFinished:
		return "session finished"
	case types.StatusPaused:
		return "session paused"
	case types.StatusStuck:
		return "session stopped"
	default:
		return "session completed"
	}
}

// sessionRunContextError 将上下文错误标准化为 session 运行阶段的领域错误文案。
func sessionRunContextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	err := ctx.Err()
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("session run timeout exceeded: %w", err)
	}
	return fmt.Errorf("session run canceled: %w", err)
}
