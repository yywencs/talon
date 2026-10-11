package tools

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/scenario"
	"github.com/wen/opentalon/internal/simulator"
)

// toolops-v3 required_evidence 静态可达性测试：对每个场景推进到正确调查路径的
// 时刻，调用真实只读查询，并用 evidence.go 的 canonicalEvidenceIDs 断言每个
// required_evidence 确实可由只读工具结果产生（不引入当前代码不会产生的 ID）。

type v3EvidenceRead struct {
	kind string // metrics | logs | traces | providers | changes | credentials | connections
	want string
}

type v3EvidenceRow struct {
	version string
	id      string
	at      time.Duration
	reads   []v3EvidenceRead
}

func loadV3EvidenceDataset(t *testing.T, version string) *scenario.Dataset {
	t.Helper()
	item, err := scenario.LoadDataset("../../data/" + version)
	require.NoError(t, err)
	return item
}

func TestV3RequiredEvidenceReachable(t *testing.T) {
	rows := []v3EvidenceRow{
		{version: "toolops-v3-validation", id: "incident-v3-d01", at: 7 * time.Minute, reads: []v3EvidenceRead{
			{kind: "metrics", want: "metric.error_rate_by_route"},
			{kind: "logs", want: "log.invalid_parameter_type"},
			{kind: "traces", want: "trace.provider_request_not_sent"},
			{kind: "changes", want: "change.mapping_v3_publish"},
			{kind: "changes", want: "change.mapping_v4_publish"},
		}},
		{version: "toolops-v3-validation", id: "incident-v3-d02", at: 5 * time.Minute, reads: []v3EvidenceRead{
			{kind: "metrics", want: "metric.connection_error_rate_by_route"},
			{kind: "logs", want: "log.connection_refused"},
			{kind: "traces", want: "trace.peer_address_observed"},
			{kind: "providers", want: "provider.endpoint_healthy"},
			{kind: "connections", want: "connection.resolver_cache_generation"},
		}},
		{version: "toolops-v3-validation", id: "incident-v3-d03", at: 5 * time.Minute, reads: []v3EvidenceRead{
			{kind: "metrics", want: "metric.connection_error_rate_by_route"},
			{kind: "logs", want: "log.connection_refused"},
			{kind: "traces", want: "trace.peer_address_observed"},
			{kind: "providers", want: "provider.endpoint_healthy"},
			{kind: "connections", want: "connection.resolver_cache_generation"},
		}},
		{version: "toolops-v3-validation", id: "incident-v3-d04", at: 11 * time.Minute, reads: []v3EvidenceRead{
			{kind: "metrics", want: "metric.error_rate_by_route"},
			{kind: "logs", want: "log.upstream_gc_pause"},
			{kind: "logs", want: "log.upstream_gc_pause_recovered"},
			{kind: "providers", want: "provider.endpoint_healthy"},
		}},
		{version: "toolops-v3-validation", id: "incident-v3-d05", at: 7 * time.Minute, reads: []v3EvidenceRead{
			{kind: "metrics", want: "metric.error_rate_by_route"},
			{kind: "logs", want: "log.price_tier_adjusted"},
			{kind: "providers", want: "provider.endpoint_healthy"},
		}},
		{version: "toolops-v3-test", id: "incident-v3-h01", at: 6 * time.Minute, reads: []v3EvidenceRead{
			{kind: "metrics", want: "metric.error_rate_by_route"},
			{kind: "logs", want: "log.upstream_unavailable"},
			{kind: "logs", want: "log.failover_completed"},
			{kind: "providers", want: "provider.endpoint_healthy"},
			{kind: "traces", want: "trace.peer_address_observed"},
		}},
		{version: "toolops-v3-test", id: "incident-v3-h02", at: 5 * time.Minute, reads: []v3EvidenceRead{
			{kind: "metrics", want: "metric.connection_error_rate_by_route"},
			{kind: "logs", want: "log.connection_refused"},
			{kind: "traces", want: "trace.peer_address_observed"},
			{kind: "providers", want: "provider.endpoint_healthy"},
			{kind: "connections", want: "connection.resolver_cache_generation"},
		}},
		{version: "toolops-v3-test", id: "incident-v3-h03", at: 5 * time.Minute, reads: []v3EvidenceRead{
			{kind: "metrics", want: "metric.error_rate_by_route"},
			{kind: "logs", want: "log.invalid_response_schema"},
			{kind: "changes", want: "change.mapping_v3_publish"},
		}},
		{version: "toolops-v3-test", id: "incident-v3-h04", at: 5 * time.Minute, reads: []v3EvidenceRead{
			{kind: "metrics", want: "metric.connection_error_rate_by_route"},
			{kind: "logs", want: "log.connection_refused"},
			{kind: "traces", want: "trace.provider_request_not_sent"},
			{kind: "providers", want: "provider.endpoint_healthy"},
		}},
		{version: "toolops-v3-test", id: "incident-v3-h05", at: 5 * time.Minute, reads: []v3EvidenceRead{
			{kind: "metrics", want: "metric.authentication_error_rate"},
			{kind: "logs", want: "log.provider_unauthorized"},
			{kind: "traces", want: "trace.provider_status_401"},
			{kind: "providers", want: "provider.endpoint_healthy"},
		}},
		{version: "toolops-v3-test", id: "incident-v3-h06", at: 9 * time.Minute, reads: []v3EvidenceRead{
			{kind: "metrics", want: "metric.error_rate_by_route"},
			{kind: "logs", want: "log.invalid_parameter_type"},
			{kind: "logs", want: "log.auto_rollback_applied"},
			{kind: "traces", want: "trace.provider_request_not_sent"},
			{kind: "changes", want: "change.mapping_v3_publish"},
			{kind: "changes", want: "change.mapping_v1_publish"},
		}},
		{version: "toolops-v3-test", id: "incident-v3-h07", at: 5 * time.Minute, reads: []v3EvidenceRead{
			{kind: "metrics", want: "metric.error_rate_by_route"},
			{kind: "logs", want: "log.response_payload_corrupted"},
			{kind: "logs", want: "log.data_integrity_alert"},
			{kind: "traces", want: "trace.peer_address_observed"},
			{kind: "providers", want: "provider.endpoint_healthy"},
		}},
		{version: "toolops-v3-test", id: "incident-v3-h08", at: 5 * time.Minute, reads: []v3EvidenceRead{
			{kind: "metrics", want: "metric.connection_error_rate_by_route"},
			{kind: "logs", want: "log.connection_refused"},
			{kind: "logs", want: "log.pool_sessions_exhausted"},
			{kind: "traces", want: "trace.peer_address_observed"},
			{kind: "providers", want: "provider.endpoint_healthy"},
		}},
		{version: "toolops-v3-test", id: "incident-v3-h09", at: 5 * time.Minute, reads: []v3EvidenceRead{
			{kind: "metrics", want: "metric.connection_error_rate_by_route"},
			{kind: "logs", want: "log.connection_refused"},
			{kind: "logs", want: "log.pool_corruption_detected"},
			{kind: "traces", want: "trace.peer_address_observed"},
			{kind: "connections", want: "connection.resolver_cache_generation"},
		}},
		{version: "toolops-v3-test", id: "incident-v3-h10", at: 8 * time.Minute, reads: []v3EvidenceRead{
			{kind: "metrics", want: "metric.error_rate_by_route"},
			{kind: "logs", want: "log.quota_exceeded"},
			{kind: "logs", want: "log.provider_unauthorized"},
			{kind: "traces", want: "trace.provider_status_401"},
			{kind: "credentials", want: "credential.status_invalid"},
		}},
		// h10 的轮换日志在 8m30s 事件后可见（首探 hard_stop 后的回查时刻）。
		{version: "toolops-v3-test", id: "incident-v3-h10", at: 10 * time.Minute, reads: []v3EvidenceRead{
			{kind: "logs", want: "log.credential_rotation_applied"},
		}},
		{version: "toolops-v3-test", id: "incident-v3-h11", at: 6 * time.Minute, reads: []v3EvidenceRead{
			{kind: "metrics", want: "metric.error_rate_by_route"},
			{kind: "logs", want: "log.invalid_parameter_type"},
			{kind: "logs", want: "log.connection_refused"},
			{kind: "traces", want: "trace.provider_request_not_sent"},
			{kind: "traces", want: "trace.peer_address_observed"},
			{kind: "changes", want: "change.mapping_v2_publish"},
			{kind: "connections", want: "connection.resolver_cache_generation"},
		}},
		// h12 的 trace.peer_address_obsolete 是 evaluator 派生 ID（需同时引用 Trace 与
		// Provider 真值），静态侧只断言可产生 observed 与两侧原始事实。
		{version: "toolops-v3-test", id: "incident-v3-h12", at: 6 * time.Minute, reads: []v3EvidenceRead{
			{kind: "metrics", want: "metric.error_rate_by_route"},
			{kind: "metrics", want: "metric.connection_error_rate_by_route"},
			{kind: "logs", want: "log.connection_refused"},
			{kind: "logs", want: "log.pool_maintenance_completed"},
			{kind: "traces", want: "trace.peer_address_observed"},
			{kind: "providers", want: "provider.endpoint_healthy"},
			{kind: "connections", want: "connection.resolver_cache_generation"},
		}},
		{version: "toolops-v3-test", id: "incident-v3-h13", at: 5 * time.Minute, reads: []v3EvidenceRead{
			{kind: "metrics", want: "metric.error_rate_by_route"},
			{kind: "logs", want: "log.invalid_parameter_type"},
			{kind: "logs", want: "log.initial_release"},
			{kind: "traces", want: "trace.provider_request_not_sent"},
			{kind: "changes", want: "change.mapping_v1_publish"},
		}},
	}

	for _, row := range rows {
		t.Run(row.id+"/"+row.at.String(), func(t *testing.T) {
			dataset := loadV3EvidenceDataset(t, row.version)
			item, ok := dataset.Find(row.id)
			require.True(t, ok)
			sim, err := simulator.New(item.Scenario)
			require.NoError(t, err)
			ctx := context.Background()
			require.NoError(t, sim.Advance(ctx, row.at))
			incidentID := item.Scenario.Metadata.ID
			scope := platform.Scope{IncidentID: incidentID}

			for _, read := range row.reads {
				produced := v3ReadEvidenceIDs(t, sim, ctx, read.kind, scope)
				require.Contains(t, produced, read.want,
					"%s: %s 查询应产生 required evidence %q", row.id, read.kind, read.want)
			}
		})
	}
}

func v3ReadEvidenceIDs(t *testing.T, sim *simulator.Simulator, ctx context.Context, kind string, scope platform.Scope) []string {
	t.Helper()
	var result []string
	switch kind {
	case "metrics":
		metrics, err := sim.QueryMetrics(ctx, platform.MetricQuery{Scope: scope})
		require.NoError(t, err)
		result = canonicalEvidenceIDs(metrics)
	case "logs":
		logs, err := sim.QueryLogs(ctx, platform.LogQuery{Scope: scope})
		require.NoError(t, err)
		result = canonicalEvidenceIDs(logs)
	case "traces":
		traces, err := sim.QueryTraces(ctx, platform.TraceQuery{Scope: scope})
		require.NoError(t, err)
		result = canonicalEvidenceIDs(traces)
	case "providers":
		providers, err := sim.GetProviders(ctx, platform.StateQuery{Scope: scope})
		require.NoError(t, err)
		result = canonicalEvidenceIDs(providers)
	case "changes":
		changes, err := sim.GetChangeRecords(ctx, platform.ChangeQuery{Scope: scope})
		require.NoError(t, err)
		result = canonicalEvidenceIDs(changes)
	case "credentials":
		credentials, err := sim.GetCredentialMetadata(ctx, platform.StateQuery{Scope: scope})
		require.NoError(t, err)
		result = canonicalEvidenceIDs(credentials)
	case "connections":
		connections, err := sim.GetConnectionMetadata(ctx, platform.StateQuery{Scope: scope})
		require.NoError(t, err)
		result = canonicalEvidenceIDs(connections)
	default:
		t.Fatalf("unknown read kind %q", kind)
	}
	return result
}
