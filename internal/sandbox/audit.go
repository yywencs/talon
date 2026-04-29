package sandbox

import (
	"context"
	"time"

	"github.com/wen/opentalon/pkg/observability"
)

var tracerForSandbox = func() observability.Tracer {
	return observability.TracerFor("internal/sandbox")
}

type execAudit struct {
	Runtime           string
	ContainerID       string
	Image             string
	WorkspaceRoot     string
	CWD               string
	Command           string
	ExitCode          int
	TimedOut          bool
	OutputTruncated   bool
	StdoutBytes       int
	StderrBytes       int
	ErrorReason       string
	Cancelled         bool
	MemoryLimitBytes  int64
	PIDLimit          int
	RecoveryAttempted bool
	RecoveryResult    string
}

type cleanupAudit struct {
	Runtime          string
	ContainerID      string
	Image            string
	MemoryLimitBytes int64
	PIDLimit         int
	CleanupReason    string
	IdleDuration     time.Duration
	CleanupResult    string
}

func startSandboxExecSpan(ctx context.Context, sandboxID string, audit execAudit) (context.Context, observability.Span) {
	ctx, span := tracerForSandbox().StartSpan(ctx, "sandbox.exec",
		observability.WithSpanKind(observability.SpanKindInternal),
	)
	span.SetAttributes(
		observability.String("sandbox.id", sandboxID),
		observability.String("sandbox.runtime", audit.Runtime),
		observability.String("sandbox.container.id", audit.ContainerID),
		observability.String("sandbox.image", audit.Image),
		observability.String("sandbox.workspace_root", audit.WorkspaceRoot),
		observability.String("sandbox.cwd", audit.CWD),
		observability.String("sandbox.command", audit.Command),
		observability.Int64("sandbox.memory_limit_bytes", audit.MemoryLimitBytes),
		observability.Int("sandbox.pid_limit", audit.PIDLimit),
	)
	return ctx, span
}

func finishSandboxExecSpan(span observability.Span, audit execAudit, err error) {
	if span == nil {
		return
	}

	span.SetAttributes(
		observability.Int("sandbox.exit_code", audit.ExitCode),
		observability.Bool("sandbox.timed_out", audit.TimedOut),
		observability.Bool("sandbox.output_truncated", audit.OutputTruncated),
		observability.Int("sandbox.stdout_bytes", audit.StdoutBytes),
		observability.Int("sandbox.stderr_bytes", audit.StderrBytes),
		observability.String("sandbox.error_reason", audit.ErrorReason),
		observability.Bool("sandbox.recovery_attempted", audit.RecoveryAttempted),
		observability.String("sandbox.recovery_result", audit.RecoveryResult),
	)
	if audit.OutputTruncated {
		span.AddEvent("sandbox.output.truncated",
			observability.Int("sandbox.stdout_bytes", audit.StdoutBytes),
			observability.Int("sandbox.stderr_bytes", audit.StderrBytes),
		)
	}
	if audit.RecoveryAttempted {
		span.AddEvent("sandbox.recovery",
			observability.String("sandbox.recovery_result", audit.RecoveryResult),
		)
	}
	if audit.TimedOut {
		span.SetStatus(observability.SpanStatusTimeout, "sandbox execution timed out")
	} else if audit.Cancelled {
		span.SetStatus(observability.SpanStatusCancelled, "sandbox execution cancelled")
	} else if err != nil {
		span.RecordError(err, observability.SpanStatusError)
	} else {
		span.SetStatus(observability.SpanStatusOK, "sandbox execution completed")
	}
}

func recordSandboxCleanup(ctx context.Context, sandboxID string, audit cleanupAudit, err error) {
	_, span := tracerForSandbox().StartSpan(ctx, "sandbox.cleanup",
		observability.WithSpanKind(observability.SpanKindInternal),
	)
	if span == nil {
		return
	}
	defer span.End()

	span.SetAttributes(
		observability.String("sandbox.id", sandboxID),
		observability.String("sandbox.runtime", audit.Runtime),
		observability.String("sandbox.container.id", audit.ContainerID),
		observability.String("sandbox.image", audit.Image),
		observability.Int64("sandbox.memory_limit_bytes", audit.MemoryLimitBytes),
		observability.Int("sandbox.pid_limit", audit.PIDLimit),
		observability.String("sandbox.cleanup_reason", audit.CleanupReason),
		observability.Int64("sandbox.idle_duration_ms", audit.IdleDuration.Milliseconds()),
		observability.String("sandbox.cleanup_result", audit.CleanupResult),
	)
	if err != nil {
		span.RecordError(err, observability.SpanStatusError)
		return
	}
	span.SetStatus(observability.SpanStatusOK, "sandbox cleanup completed")
}
