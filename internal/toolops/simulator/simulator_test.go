package simulator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/toolops/platform"
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
	require.Equal(t, "healthy", probe.Result["outcome"])

	recovery, err := simulator.RequestRecovery(ctx, platform.RecoveryRequest{
		IncidentID: item.Scenario.Metadata.ID,
		PolicyID:   "default-safe-recovery", IdempotencyKey: "recovery-001",
	})
	require.NoError(t, err)
	require.Equal(t, platform.OperationSucceeded, recovery.Status)
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

	escalation, err := simulator.EscalateIncident(ctx, platform.EscalationRequest{
		IncidentID: item.Scenario.Metadata.ID, Reason: "no_safe_remediation_available",
		EvidenceRefs:          []string{"log:provider_unauthorized", "credential:credential-doc-a-v7"},
		AttemptedOperationIDs: []string{operation.ID}, IdempotencyKey: "escalate-001",
	})
	require.NoError(t, err)
	require.Equal(t, "platform-security-oncall", escalation.Result["destination"])
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
	require.Equal(t, "healthy", secondProbe.Result["outcome"])
	_, err = simulator.RequestRecovery(ctx, platform.RecoveryRequest{
		IncidentID: item.Scenario.Metadata.ID,
		PolicyID:   "default-safe-recovery", IdempotencyKey: "connection-recovery-001",
	})
	require.NoError(t, err)
	require.Equal(t, 70, simulator.Snapshot().Routes["route-a"].Weight)
}

func TestSimulatorCannotAdvancePastScenarioEnd(t *testing.T) {
	item := findTestCase(t, "mapping-regression-rollback-001")
	simulator, err := New(item.Scenario)
	require.NoError(t, err)
	require.ErrorContains(t, simulator.Advance(context.Background(), 41*time.Minute), "exceeds world end time")
}
