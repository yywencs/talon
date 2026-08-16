package simulator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/platform"
)

func TestSimulatorCompletesMappingRollbackFlow(t *testing.T) {
	item := findTestCase(t, "mapping-regression-rollback-001")
	simulator, err := New(item.Scenario)
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, simulator.Advance(ctx, 5*time.Minute))
	snapshot := simulator.Snapshot()
	require.Equal(t, 10, snapshot.Routes["route-a"].Weight)
	require.Equal(t, 90, snapshot.Routes["route-b"].Weight)
	require.True(t, snapshot.Configs["mapping-v2"].Active)
	require.Equal(t, 0.78, snapshot.Traffic.SuccessRate)
	require.Len(t, snapshot.Logs, 1)
	require.Len(t, snapshot.Traces, 1)
	require.Len(t, snapshot.Changes, 1)

	policies, err := simulator.GetRecoveryPolicies(ctx, platform.StateQuery{
		Scope: platform.Scope{IncidentID: item.Scenario.Metadata.ID},
	})
	require.NoError(t, err)
	require.Len(t, policies, 1)
	require.Equal(t, "default-safe-recovery", policies[0].ID)
	require.Equal(t, []float64{0.01, 0.05}, policies[0].ProbeSteps)
	require.Equal(t, []float64{0.10, 0.25, 0.50, 1.00}, policies[0].RecoverySteps)
	require.Equal(t, true, policies[0].HardStopWhen["new_error_type"])

	operation, err := simulator.ExecuteRemediation(ctx, platform.RemediationRequest{
		IncidentID: item.Scenario.Metadata.ID,
		ToolName:   "rollback_mapping",
		Arguments: map[string]any{
			"tool_id":        "generate_image",
			"target_version": "mapping-v1",
		},
		ExpectedVersion: "mapping-v2",
		IdempotencyKey:  "rollback-001",
	})
	require.NoError(t, err)
	require.Equal(t, platform.OperationPending, operation.Status)

	tasks, err := simulator.GetTasks(ctx, platform.TaskQuery{Scope: platform.Scope{IncidentID: item.Scenario.Metadata.ID}})
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, platform.TaskProcessing, tasks[0].Status)

	repeated, err := simulator.ExecuteRemediation(ctx, platform.RemediationRequest{
		IncidentID:     item.Scenario.Metadata.ID,
		ToolName:       "rollback_mapping",
		Arguments:      map[string]any{"tool_id": "ignored"},
		IdempotencyKey: "rollback-001",
	})
	require.NoError(t, err)
	require.Equal(t, operation.ID, repeated.ID)

	require.NoError(t, simulator.Advance(ctx, time.Minute))
	operation, err = simulator.GetOperation(ctx, platform.OperationQuery{
		IncidentID: item.Scenario.Metadata.ID, OperationID: operation.ID,
	})
	require.NoError(t, err)
	require.Equal(t, platform.OperationSucceeded, operation.Status)
	require.True(t, simulator.Snapshot().Configs["mapping-v1"].Active)

	probe, err := simulator.RequestProbe(ctx, platform.ProbeRequest{
		IncidentID: item.Scenario.Metadata.ID, RouteID: "route-a",
		PolicyID: "default-safe-recovery", IdempotencyKey: "probe-001",
	})
	require.NoError(t, err)
	require.Equal(t, platform.OperationPending, probe.Status)
	require.Equal(t, "pending", probe.Result["outcome"])
	// 探测流量独立记录在 ProbeSession 中，不直接修改受保护路由的正式权重。
	require.Equal(t, 10, simulator.Snapshot().Routes["route-a"].Weight)
	require.NoError(t, simulator.Advance(ctx, time.Minute))
	probe, err = simulator.GetOperation(ctx, platform.OperationQuery{
		IncidentID: item.Scenario.Metadata.ID, OperationID: probe.ID,
	})
	require.NoError(t, err)
	require.Equal(t, platform.OperationRunning, probe.Status)
	require.Equal(t, "running", probe.Result["outcome"])
	require.NoError(t, simulator.Advance(ctx, 5*time.Minute))
	probe, err = simulator.GetOperation(ctx, platform.OperationQuery{
		IncidentID: item.Scenario.Metadata.ID, OperationID: probe.ID,
	})
	require.NoError(t, err)
	require.Equal(t, platform.OperationSucceeded, probe.Status)
	require.Equal(t, "healthy", probe.Result["outcome"])
	windows, ok := probe.Result["windows"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, windows, 6)
	require.Equal(t, 0.01, windows[0]["traffic_fraction"])
	require.Equal(t, 0.05, windows[5]["traffic_fraction"])

	recovery, err := simulator.RequestRecovery(ctx, platform.RecoveryRequest{
		IncidentID: item.Scenario.Metadata.ID, RouteID: "route-a",
		PolicyID: "default-safe-recovery", IdempotencyKey: "recovery-001",
	})
	require.NoError(t, err)
	require.Equal(t, platform.OperationPending, recovery.Status)
	require.Equal(t, 10, simulator.Snapshot().Routes["route-a"].Weight)
	require.NoError(t, simulator.Advance(ctx, 3*time.Minute))
	require.Equal(t, 20, simulator.Snapshot().Routes["route-a"].Weight)
	require.NoError(t, simulator.Advance(ctx, 9*time.Minute))
	recovery, err = simulator.GetOperation(ctx, platform.OperationQuery{
		IncidentID: item.Scenario.Metadata.ID, OperationID: recovery.ID,
	})
	require.NoError(t, err)
	require.Equal(t, platform.OperationSucceeded, recovery.Status)
	require.Equal(t, "healthy", recovery.Result["outcome"])
	require.Len(t, recovery.Result["windows"], 12)
	snapshot = simulator.Snapshot()
	require.Equal(t, 80, snapshot.Routes["route-a"].Weight)
	require.Equal(t, 20, snapshot.Routes["route-b"].Weight)
}

func TestSimulatorRejectsCredentialMutationAndEscalates(t *testing.T) {
	item := findTestCase(t, "credential-revoked-escalation-001")
	simulator, err := New(item.Scenario)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, simulator.Advance(ctx, 4*time.Minute))

	snapshot := simulator.Snapshot()
	require.Equal(t, 0, snapshot.Routes["route-primary"].Weight)
	require.Equal(t, 0, snapshot.Routes["route-fallback"].Weight)
	require.Equal(t, "invalid", snapshot.Credentials["provider-doc-a"].Status)

	operation, err := simulator.ExecuteRemediation(ctx, platform.RemediationRequest{
		IncidentID: item.Scenario.Metadata.ID,
		ToolName:   "rotate_provider_credential",
		Arguments: map[string]any{
			"provider_id": "provider-doc-a", "credential_id": "credential-doc-a-v7",
			"secret_reference": "vault://replacement",
		},
		IdempotencyKey: "rotate-001",
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, platform.ErrUnauthorized))
	require.Equal(t, platform.OperationRejected, operation.Status)

	probe, err := simulator.RequestProbe(ctx, platform.ProbeRequest{
		IncidentID: item.Scenario.Metadata.ID, RouteID: "route-primary",
		PolicyID: "auth-recovery", IdempotencyKey: "probe-invalid-credential",
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, platform.ErrPreconditionFailed))
	require.Equal(t, platform.OperationRejected, probe.Status)

	rejectedEscalation, err := simulator.EscalateIncident(ctx, platform.EscalationRequest{
		IncidentID: item.Scenario.Metadata.ID, Reason: "缺少结构化升级类别",
		EvidenceRefs: []string{"log:provider_unauthorized"}, IdempotencyKey: "escalate-missing-code",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, platform.ErrPreconditionFailed)
	require.Equal(t, platform.OperationRejected, rejectedEscalation.Status)

	escalation, err := simulator.EscalateIncident(ctx, platform.EscalationRequest{
		IncidentID: item.Scenario.Metadata.ID, ReasonCode: platform.EscalationReasonNoSafeRemediationAvailable,
		Reason:                "凭据由外部系统管理，当前没有安全自动修复能力",
		EvidenceRefs:          []string{"log:provider_unauthorized", "credential:credential-doc-a-v7"},
		AttemptedOperationIDs: []string{operation.ID}, IdempotencyKey: "escalate-001",
	})
	require.NoError(t, err)
	require.Equal(t, "platform-security-oncall", escalation.Result["destination"])
	require.Equal(t, "no_safe_remediation_available", escalation.Result["reason_code"])
}

func TestSimulatorUsesFailedProbeEvidenceBeforeRecreatingPool(t *testing.T) {
	item := findTestCase(t, "connection-recovery-two-cycles-001")
	simulator, err := New(item.Scenario)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, simulator.Advance(ctx, 5*time.Minute))
	require.Equal(t, 10, simulator.Snapshot().Routes["route-a"].Weight)

	refresh, err := simulator.ExecuteRemediation(ctx, platform.RemediationRequest{
		IncidentID: item.Scenario.Metadata.ID, ToolName: "refresh_provider_connection",
		Arguments:      map[string]any{"provider_id": "provider-search-a", "expected_pool_generation": 12},
		IdempotencyKey: "refresh-001",
	})
	require.NoError(t, err)
	require.Equal(t, platform.OperationPending, refresh.Status)
	require.NoError(t, simulator.Advance(ctx, time.Minute))
	require.Equal(t, 13, simulator.Snapshot().Connections["provider-search-a"].PoolGeneration)

	firstProbe, err := simulator.RequestProbe(ctx, platform.ProbeRequest{
		IncidentID: item.Scenario.Metadata.ID, RouteID: "route-a",
		PolicyID: "default-safe-recovery", IdempotencyKey: "connection-probe-001",
	})
	require.NoError(t, err)
	require.Equal(t, platform.OperationPending, firstProbe.Status)
	require.NoError(t, simulator.Advance(ctx, time.Minute))
	firstProbe, err = simulator.GetOperation(ctx, platform.OperationQuery{
		IncidentID: item.Scenario.Metadata.ID, OperationID: firstProbe.ID,
	})
	require.NoError(t, err)
	require.Equal(t, platform.OperationSucceeded, firstProbe.Status)
	require.Equal(t, "hard_stop", firstProbe.Result["outcome"])
	logs, err := simulator.QueryLogs(ctx, platform.LogQuery{
		Scope: platform.Scope{IncidentID: item.Scenario.Metadata.ID, RouteID: "route-a"},
		Range: platform.TimeRange{From: simulator.Snapshot().Now.Add(-time.Minute), To: simulator.Snapshot().Now},
	})
	require.NoError(t, err)
	require.NotEmpty(t, logs)
	require.Equal(t, "resolver_cache_hit", logs[len(logs)-1].Code)

	recreate, err := simulator.ExecuteRemediation(ctx, platform.RemediationRequest{
		IncidentID: item.Scenario.Metadata.ID, ToolName: "recreate_provider_connection_pool",
		Arguments:      map[string]any{"provider_id": "provider-search-a", "expected_pool_generation": 13},
		IdempotencyKey: "recreate-001",
	})
	require.NoError(t, err)
	require.Equal(t, platform.OperationPending, recreate.Status)
	require.NoError(t, simulator.Advance(ctx, 2*time.Minute))
	connection := simulator.Snapshot().Connections["provider-search-a"]
	require.Equal(t, 14, connection.PoolGeneration)
	require.Equal(t, 5, connection.ResolverCacheGeneration)
	require.Equal(t, "192.0.2.25", connection.ResolvedIP)

	secondProbe, err := simulator.RequestProbe(ctx, platform.ProbeRequest{
		IncidentID: item.Scenario.Metadata.ID, RouteID: "route-a",
		PolicyID: "default-safe-recovery", IdempotencyKey: "connection-probe-002",
	})
	require.NoError(t, err)
	require.Equal(t, platform.OperationPending, secondProbe.Status)
	require.NoError(t, simulator.Advance(ctx, 6*time.Minute))
	secondProbe, err = simulator.GetOperation(ctx, platform.OperationQuery{
		IncidentID: item.Scenario.Metadata.ID, OperationID: secondProbe.ID,
	})
	require.NoError(t, err)
	require.Equal(t, "healthy", secondProbe.Result["outcome"])
	_, err = simulator.RequestRecovery(ctx, platform.RecoveryRequest{
		IncidentID: item.Scenario.Metadata.ID, RouteID: "route-a",
		PolicyID: "default-safe-recovery", IdempotencyKey: "connection-recovery-001",
	})
	require.NoError(t, err)
	require.NoError(t, simulator.Advance(ctx, 12*time.Minute))
	require.Equal(t, 70, simulator.Snapshot().Routes["route-a"].Weight)
}

func TestSimulatorRecoveryHardStopReturnsToProtectedWeight(t *testing.T) {
	item := findTestCase(t, "mapping-regression-rollback-001")
	item.Scenario.ActionBehavior["request_recovery"] = map[string]any{
		"result": "accepted", "window_duration": "1m",
		"steps": []any{
			map[string]any{"sample_count": 100, "success_rate": 0.99, "latency_p95_ms": 1200, "cost_per_success": 0.04, "telemetry_complete": true, "error_types": []any{}},
			map[string]any{"sample_count": 100, "success_rate": 0.90, "latency_p95_ms": 1200, "cost_per_success": 0.04, "telemetry_complete": true, "error_types": []any{"regression_returned"}},
		},
	}
	simulator, err := New(item.Scenario)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, simulator.Advance(ctx, 5*time.Minute))
	rollback, err := simulator.ExecuteRemediation(ctx, platform.RemediationRequest{
		IncidentID: item.Scenario.Metadata.ID, ToolName: "rollback_mapping",
		Arguments:       map[string]any{"tool_id": "generate_image", "target_version": "mapping-v1"},
		ExpectedVersion: "mapping-v2", IdempotencyKey: "recovery-hard-stop-rollback",
	})
	require.NoError(t, err)
	require.Equal(t, platform.OperationPending, rollback.Status)
	require.NoError(t, simulator.Advance(ctx, time.Minute))
	probe, err := simulator.RequestProbe(ctx, platform.ProbeRequest{
		IncidentID: item.Scenario.Metadata.ID, RouteID: "route-a",
		PolicyID: "default-safe-recovery", IdempotencyKey: "recovery-hard-stop-probe",
	})
	require.NoError(t, err)
	require.NoError(t, simulator.Advance(ctx, 6*time.Minute))
	probe, err = simulator.GetOperation(ctx, platform.OperationQuery{IncidentID: item.Scenario.Metadata.ID, OperationID: probe.ID})
	require.NoError(t, err)
	require.Equal(t, "healthy", probe.Result["outcome"])

	recovery, err := simulator.RequestRecovery(ctx, platform.RecoveryRequest{
		IncidentID: item.Scenario.Metadata.ID, RouteID: "route-a",
		PolicyID: "default-safe-recovery", IdempotencyKey: "recovery-hard-stop",
	})
	require.NoError(t, err)
	require.NoError(t, simulator.Advance(ctx, 3*time.Minute))
	require.Equal(t, 20, simulator.Snapshot().Routes["route-a"].Weight)
	require.NoError(t, simulator.Advance(ctx, time.Minute))
	recovery, err = simulator.GetOperation(ctx, platform.OperationQuery{IncidentID: item.Scenario.Metadata.ID, OperationID: recovery.ID})
	require.NoError(t, err)
	require.Equal(t, platform.OperationSucceeded, recovery.Status)
	require.Equal(t, "hard_stop", recovery.Result["outcome"])
	require.Contains(t, recovery.Result["reason"], "error_rate")
	require.Equal(t, 10, simulator.Snapshot().Routes["route-a"].Weight)
	require.Equal(t, 90, simulator.Snapshot().Routes["route-b"].Weight)
}

func TestSimulatorCannotAdvancePastScenarioEnd(t *testing.T) {
	item := findTestCase(t, "mapping-regression-rollback-001")
	simulator, err := New(item.Scenario)
	require.NoError(t, err)
	require.ErrorContains(t, simulator.Advance(context.Background(), 41*time.Minute), "exceeds world end time")
}

func TestSimulatorProbeRequiresSamplesAndConsecutiveHealthyWindows(t *testing.T) {
	item := findTestCase(t, "mapping-regression-rollback-001")
	item.Scenario.ActionBehavior["request_probe"] = map[string]any{
		"result": "accepted", "window_duration": "1m",
		"attempts": []any{
			map[string]any{
				"steps": []any{
					map[string]any{
						"sample_count": 40, "success_rate": 0.99, "latency_p95_ms": 1200,
						"cost_per_success": 0.04, "telemetry_complete": true, "error_types": []any{},
					},
					map[string]any{
						"sample_count": 100, "success_rate": 0.99, "latency_p95_ms": 1200,
						"cost_per_success": 0.04, "telemetry_complete": false, "error_types": []any{},
					},
				},
			},
		},
	}
	simulator, err := New(item.Scenario)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, simulator.Advance(ctx, 5*time.Minute))
	probe, err := simulator.RequestProbe(ctx, platform.ProbeRequest{
		IncidentID: item.Scenario.Metadata.ID, RouteID: "route-a",
		PolicyID: "default-safe-recovery", IdempotencyKey: "probe-window-gates",
	})
	require.NoError(t, err)

	require.NoError(t, simulator.Advance(ctx, 2*time.Minute))
	probe, err = simulator.GetOperation(ctx, platform.OperationQuery{IncidentID: item.Scenario.Metadata.ID, OperationID: probe.ID})
	require.NoError(t, err)
	require.Equal(t, platform.OperationRunning, probe.Status)
	require.Equal(t, 1, probe.Result["current_step"])
	require.Equal(t, 80, probe.Result["step_sample_count"])
	require.Equal(t, 2, probe.Result["healthy_windows"])

	require.NoError(t, simulator.Advance(ctx, time.Minute))
	probe, err = simulator.GetOperation(ctx, platform.OperationQuery{IncidentID: item.Scenario.Metadata.ID, OperationID: probe.ID})
	require.NoError(t, err)
	require.Equal(t, 2, probe.Result["current_step"])
	require.Equal(t, 0.05, probe.Result["traffic_fraction"])

	require.NoError(t, simulator.Advance(ctx, 3*time.Minute))
	probe, err = simulator.GetOperation(ctx, platform.OperationQuery{IncidentID: item.Scenario.Metadata.ID, OperationID: probe.ID})
	require.NoError(t, err)
	require.Equal(t, platform.OperationRunning, probe.Status)
	require.Equal(t, 0, probe.Result["healthy_windows"])
	windows := probe.Result["windows"].([]map[string]any)
	require.Contains(t, windows[len(windows)-1]["failed_requirements"], "telemetry_complete")
}
