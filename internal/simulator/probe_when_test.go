package simulator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/platform"
)

// 探针剧本页 when 谓词与世界状态绑定的机制测试：健康页必须等对应条件
// （修复落地或自愈事件触发）才生效，修复缺位时探测持续失败。

func probeOutcome(t *testing.T, simulator *Simulator, incidentID, operationID string) string {
	t.Helper()
	operation, err := simulator.GetOperation(context.Background(), platform.OperationQuery{
		IncidentID: incidentID, OperationID: operationID,
	})
	require.NoError(t, err)
	outcome, _ := operation.Result["outcome"].(string)
	return outcome
}

func TestProbeHealthyPageGatedByWorldEffect(t *testing.T) {
	item := findVersionTwoTestCase(t, "compound-mapping-connection-001")
	simulator, err := New(item.Scenario)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, simulator.Advance(ctx, 7*time.Minute))

	// 6m 端点变更事件把 Provider 真实地址落到连接元数据，Trace 里的过时地址
	// 与它构成"对端已过时"证据来源。
	require.Equal(t, "198.51.100.90", simulator.Snapshot().Connections["provider-thumb-b"].ResolvedIP)

	// 未执行 recreate 前探测必须停留在故障页：健康页的 when 条件不满足。
	first, err := simulator.RequestProbe(ctx, platform.ProbeRequest{
		IncidentID: item.Scenario.Metadata.ID, RouteID: "route-a",
		PolicyID: "default-safe-recovery", IdempotencyKey: "when-probe-1",
	})
	require.NoError(t, err)
	require.NoError(t, simulator.Advance(ctx, 3*time.Minute))
	require.Equal(t, "hard_stop", probeOutcome(t, simulator, item.Scenario.Metadata.ID, first.ID))

	// 第二次探测（同样未修复）仍是故障页——when 模式不受调用次序影响。
	second, err := simulator.RequestProbe(ctx, platform.ProbeRequest{
		IncidentID: item.Scenario.Metadata.ID, RouteID: "route-a",
		PolicyID: "default-safe-recovery", IdempotencyKey: "when-probe-2",
	})
	require.NoError(t, err)
	require.NoError(t, simulator.Advance(ctx, 3*time.Minute))
	require.Equal(t, "hard_stop", probeOutcome(t, simulator, item.Scenario.Metadata.ID, second.ID))

	recreate, err := simulator.ExecuteRemediation(ctx, platform.RemediationRequest{
		IncidentID: item.Scenario.Metadata.ID, ToolName: "recreate_provider_connection_pool",
		Arguments:      map[string]any{"provider_id": "provider-thumb-b", "expected_pool_generation": 11},
		IdempotencyKey: "when-recreate-1",
	})
	require.NoError(t, err)
	require.Equal(t, platform.OperationPending, recreate.Status)
	require.NoError(t, simulator.Advance(ctx, 2*time.Minute))

	// recreate 的 world_effect 落地后健康页生效。
	third, err := simulator.RequestProbe(ctx, platform.ProbeRequest{
		IncidentID: item.Scenario.Metadata.ID, RouteID: "route-a",
		PolicyID: "default-safe-recovery", IdempotencyKey: "when-probe-3",
	})
	require.NoError(t, err)
	require.NoError(t, simulator.Advance(ctx, 6*time.Minute))
	require.Equal(t, "healthy", probeOutcome(t, simulator, item.Scenario.Metadata.ID, third.ID))
}

func TestProbeHealthyPageGatedByEventCause(t *testing.T) {
	item := findVersionTwoTestCase(t, "transient-timeout-recovery-001")
	simulator, err := New(item.Scenario)
	require.NoError(t, err)
	ctx := context.Background()
	// 5m 故障事件已触发、6m 自愈事件未到：故障事件与自愈事件同名同目标，
	// 谓词必须靠 internal_cause 区分。
	require.NoError(t, simulator.Advance(ctx, 5*time.Minute+30*time.Second))
	require.Equal(t, "198.51.100.77", simulator.Snapshot().Connections["provider-fetch-a"].ResolvedIP)

	first, err := simulator.RequestProbe(ctx, platform.ProbeRequest{
		IncidentID: item.Scenario.Metadata.ID, RouteID: "route-a",
		PolicyID: "default-safe-recovery", IdempotencyKey: "transient-probe-1",
	})
	require.NoError(t, err)
	require.NoError(t, simulator.Advance(ctx, 3*time.Minute))
	require.Equal(t, "hard_stop", probeOutcome(t, simulator, item.Scenario.Metadata.ID, first.ID))

	// 6m 自愈事件触发后（cause 命中），健康页生效且连接真值更新。
	require.Equal(t, "198.51.100.20", simulator.Snapshot().Connections["provider-fetch-a"].ResolvedIP)
	second, err := simulator.RequestProbe(ctx, platform.ProbeRequest{
		IncidentID: item.Scenario.Metadata.ID, RouteID: "route-a",
		PolicyID: "default-safe-recovery", IdempotencyKey: "transient-probe-2",
	})
	require.NoError(t, err)
	require.NoError(t, simulator.Advance(ctx, 4*time.Minute))
	require.Equal(t, "healthy", probeOutcome(t, simulator, item.Scenario.Metadata.ID, second.ID))
}

func TestProbeStaysUnhealthyAfterEffectlessRemediation(t *testing.T) {
	item := findVersionTwoTestCase(t, "stuck-operation-switch-001")
	simulator, err := New(item.Scenario)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, simulator.Advance(ctx, 6*time.Minute))

	// rebuild_provider_queue 没有 world_effect：即便执行成功也不解锁健康页。
	rebuild, err := simulator.ExecuteRemediation(ctx, platform.RemediationRequest{
		IncidentID: item.Scenario.Metadata.ID, ToolName: "rebuild_provider_queue",
		Arguments:      map[string]any{"provider_id": "provider-content-a"},
		IdempotencyKey: "stuck-rebuild-1",
	})
	require.NoError(t, err)
	require.Equal(t, platform.OperationPending, rebuild.Status)
	require.NoError(t, simulator.Advance(ctx, 31*time.Minute))

	probe, err := simulator.RequestProbe(ctx, platform.ProbeRequest{
		IncidentID: item.Scenario.Metadata.ID, RouteID: "route-a",
		PolicyID: "default-safe-recovery", IdempotencyKey: "stuck-probe-1",
	})
	require.NoError(t, err)
	require.NoError(t, simulator.Advance(ctx, 3*time.Minute))
	require.Equal(t, "hard_stop", probeOutcome(t, simulator, item.Scenario.Metadata.ID, probe.ID))

	force, err := simulator.ExecuteRemediation(ctx, platform.RemediationRequest{
		IncidentID: item.Scenario.Metadata.ID, ToolName: "force_queue_drain",
		Arguments:      map[string]any{"provider_id": "provider-content-a"},
		IdempotencyKey: "stuck-force-1",
	})
	require.NoError(t, err)
	require.Equal(t, platform.OperationPending, force.Status)
	require.NoError(t, simulator.Advance(ctx, 3*time.Minute))

	after, err := simulator.RequestProbe(ctx, platform.ProbeRequest{
		IncidentID: item.Scenario.Metadata.ID, RouteID: "route-a",
		PolicyID: "default-safe-recovery", IdempotencyKey: "stuck-probe-2",
	})
	require.NoError(t, err)
	require.NoError(t, simulator.Advance(ctx, 4*time.Minute))
	require.Equal(t, "healthy", probeOutcome(t, simulator, item.Scenario.Metadata.ID, after.ID))
}
