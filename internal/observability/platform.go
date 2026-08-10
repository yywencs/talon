package observability

import (
	"context"
	"fmt"
	"strings"

	"github.com/wen/opentalon/internal/platform"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const platformTracerName = "github.com/wen/opentalon/internal/observability/platform"

// ObservePlatform 使用 OpenTelemetry Span 包装 ToolOpsPlatform。
//
// 装饰器统一安装在平台边界，调用方不需要在 Agent、Workflow 或 Adapter
// 的业务代码中混入链路追踪调用。传入 nil 时仍返回 nil，便于组装可选依赖。
func ObservePlatform(next platform.ToolOpsPlatform) platform.ToolOpsPlatform {
	if next == nil {
		return nil
	}
	if _, observed := next.(*observedPlatform); observed {
		return next
	}
	return &observedPlatform{next: next}
}

type observedPlatform struct {
	next platform.ToolOpsPlatform
}

func (p *observedPlatform) QueryMetrics(ctx context.Context, query platform.MetricQuery) (platform.MetricResult, error) {
	return observePlatformCall(ctx, "query_metrics", query.Scope.IncidentID, nil, func(ctx context.Context) (platform.MetricResult, error) {
		return p.next.QueryMetrics(ctx, query)
	})
}

func (p *observedPlatform) QueryLogs(ctx context.Context, query platform.LogQuery) ([]platform.LogEntry, error) {
	return observePlatformCall(ctx, "query_logs", query.Scope.IncidentID, nil, func(ctx context.Context) ([]platform.LogEntry, error) {
		return p.next.QueryLogs(ctx, query)
	})
}

func (p *observedPlatform) QueryTraces(ctx context.Context, query platform.TraceQuery) ([]platform.TraceRecord, error) {
	return observePlatformCall(ctx, "query_traces", query.Scope.IncidentID, nil, func(ctx context.Context) ([]platform.TraceRecord, error) {
		return p.next.QueryTraces(ctx, query)
	})
}

func (p *observedPlatform) GetServices(ctx context.Context, query platform.StateQuery) ([]platform.Service, error) {
	return observePlatformCall(ctx, "get_services", query.Scope.IncidentID, nil, func(ctx context.Context) ([]platform.Service, error) {
		return p.next.GetServices(ctx, query)
	})
}

func (p *observedPlatform) GetRoutes(ctx context.Context, query platform.StateQuery) ([]platform.Route, error) {
	return observePlatformCall(ctx, "get_routes", query.Scope.IncidentID, nil, func(ctx context.Context) ([]platform.Route, error) {
		return p.next.GetRoutes(ctx, query)
	})
}

func (p *observedPlatform) GetProviders(ctx context.Context, query platform.StateQuery) ([]platform.Provider, error) {
	return observePlatformCall(ctx, "get_providers", query.Scope.IncidentID, nil, func(ctx context.Context) ([]platform.Provider, error) {
		return p.next.GetProviders(ctx, query)
	})
}

func (p *observedPlatform) GetConfigVersions(ctx context.Context, query platform.StateQuery) ([]platform.ConfigVersion, error) {
	return observePlatformCall(ctx, "get_config_versions", query.Scope.IncidentID, nil, func(ctx context.Context) ([]platform.ConfigVersion, error) {
		return p.next.GetConfigVersions(ctx, query)
	})
}

func (p *observedPlatform) GetChangeRecords(ctx context.Context, query platform.ChangeQuery) ([]platform.ChangeRecord, error) {
	return observePlatformCall(ctx, "get_change_records", query.Scope.IncidentID, nil, func(ctx context.Context) ([]platform.ChangeRecord, error) {
		return p.next.GetChangeRecords(ctx, query)
	})
}

func (p *observedPlatform) GetCredentialMetadata(ctx context.Context, query platform.StateQuery) ([]platform.CredentialMetadata, error) {
	return observePlatformCall(ctx, "get_credential_metadata", query.Scope.IncidentID, nil, func(ctx context.Context) ([]platform.CredentialMetadata, error) {
		return p.next.GetCredentialMetadata(ctx, query)
	})
}

func (p *observedPlatform) GetConnectionMetadata(ctx context.Context, query platform.StateQuery) ([]platform.ConnectionMetadata, error) {
	return observePlatformCall(ctx, "get_connection_metadata", query.Scope.IncidentID, nil, func(ctx context.Context) ([]platform.ConnectionMetadata, error) {
		return p.next.GetConnectionMetadata(ctx, query)
	})
}

func (p *observedPlatform) GetTasks(ctx context.Context, query platform.TaskQuery) ([]platform.ManagedTask, error) {
	return observePlatformCall(ctx, "get_tasks", query.Scope.IncidentID, nil, func(ctx context.Context) ([]platform.ManagedTask, error) {
		return p.next.GetTasks(ctx, query)
	})
}

func (p *observedPlatform) GetRemediationCapabilities(ctx context.Context, query platform.StateQuery) ([]platform.RemediationCapability, error) {
	return observePlatformCall(ctx, "get_remediation_capabilities", query.Scope.IncidentID, nil, func(ctx context.Context) ([]platform.RemediationCapability, error) {
		return p.next.GetRemediationCapabilities(ctx, query)
	})
}

func (p *observedPlatform) ExecuteRemediation(ctx context.Context, request platform.RemediationRequest) (platform.Operation, error) {
	return observePlatformCall(ctx, "execute_remediation", request.IncidentID, []attribute.KeyValue{
		attribute.String("toolops.remediation.name", request.ToolName),
		attribute.Bool("toolops.remediation.dry_run", request.DryRun),
	}, func(ctx context.Context) (platform.Operation, error) {
		return p.next.ExecuteRemediation(ctx, request)
	})
}

func (p *observedPlatform) RequestProbe(ctx context.Context, request platform.ProbeRequest) (platform.Operation, error) {
	return observePlatformCall(ctx, "request_probe", request.IncidentID, []attribute.KeyValue{
		attribute.String("toolops.route.id", request.RouteID),
		attribute.String("toolops.policy.id", request.PolicyID),
	}, func(ctx context.Context) (platform.Operation, error) {
		return p.next.RequestProbe(ctx, request)
	})
}

func (p *observedPlatform) RequestRecovery(ctx context.Context, request platform.RecoveryRequest) (platform.Operation, error) {
	return observePlatformCall(ctx, "request_recovery", request.IncidentID, []attribute.KeyValue{
		attribute.String("toolops.policy.id", request.PolicyID),
	}, func(ctx context.Context) (platform.Operation, error) {
		return p.next.RequestRecovery(ctx, request)
	})
}

func (p *observedPlatform) GetOperation(ctx context.Context, query platform.OperationQuery) (platform.Operation, error) {
	return observePlatformCall(ctx, "get_operation", query.IncidentID, []attribute.KeyValue{
		attribute.String("toolops.operation.id", query.OperationID),
	}, func(ctx context.Context) (platform.Operation, error) {
		return p.next.GetOperation(ctx, query)
	})
}

func (p *observedPlatform) EscalateIncident(ctx context.Context, request platform.EscalationRequest) (platform.Operation, error) {
	return observePlatformCall(ctx, "escalate_incident", request.IncidentID, nil, func(ctx context.Context) (platform.Operation, error) {
		return p.next.EscalateIncident(ctx, request)
	})
}

func observePlatformCall[T any](
	ctx context.Context,
	operation string,
	incidentID string,
	attributes []attribute.KeyValue,
	call func(context.Context) (T, error),
) (T, error) {
	operation = strings.TrimSpace(operation)
	spanAttributes := []attribute.KeyValue{
		attribute.String("toolops.platform.operation", operation),
	}
	if incidentID = strings.TrimSpace(incidentID); incidentID != "" {
		spanAttributes = append(spanAttributes, attribute.String("toolops.incident.id", incidentID))
	}
	spanAttributes = append(spanAttributes, attributes...)

	ctx, span := otel.Tracer(platformTracerName).Start(ctx, fmt.Sprintf("toolops.platform.%s", operation),
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(spanAttributes...),
	)
	defer span.End()

	result, err := call(ctx)
	if err != nil {
		recordSpanError(span, err)
		return result, err
	}
	markSpanOK(span)
	return result, nil
}
