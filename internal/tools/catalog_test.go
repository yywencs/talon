package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/scenario"
	"github.com/wen/opentalon/internal/simulator"
	"github.com/wen/opentalon/internal/workflow"
)

func TestSetBuildsIncidentScopedEinoTools(t *testing.T) {
	instance, item := newTestSimulator(t, "mapping-regression-rollback-001")
	set, err := New(context.Background(), instance, item.Scenario.Metadata.ID)
	require.NoError(t, err)

	names := toolNames(t, set)
	require.Contains(t, names, "query_metrics")
	require.Contains(t, names, "query_logs")
	require.Contains(t, names, "rollback_mapping")
	require.Contains(t, names, "request_probe")
	require.Contains(t, names, "request_recovery")
	require.Contains(t, names, "escalate_incident")
	require.NotContains(t, names, "bash")
	require.NotContains(t, names, "file_editor")

	for _, item := range set.Tools() {
		info, infoErr := item.Info(context.Background())
		require.NoError(t, infoErr)
		require.NotEmpty(t, info.Desc, info.Name)
		schemaValue, schemaErr := info.ParamsOneOf.ToJSONSchema()
		require.NoError(t, schemaErr, info.Name)
		require.NotNil(t, schemaValue, info.Name)
	}
}

func TestUnauthorizedRemediationIsNotVisible(t *testing.T) {
	instance, item := newTestSimulator(t, "credential-revoked-escalation-001")
	set, err := New(context.Background(), instance, item.Scenario.Metadata.ID)
	require.NoError(t, err)

	_, visible := set.Resolve("rotate_provider_credential")
	require.False(t, visible)
	_, bashVisible := set.Resolve("bash")
	require.False(t, bashVisible)
}

func TestDynamicRemediationHasDescriptionAndTypedSchema(t *testing.T) {
	instance, item := newTestSimulator(t, "connection-recovery-two-cycles-001")
	set, err := New(context.Background(), instance, item.Scenario.Metadata.ID)
	require.NoError(t, err)
	remediation, ok := set.Resolve("refresh_provider_connection")
	require.True(t, ok)

	info, err := remediation.Info(context.Background())
	require.NoError(t, err)
	require.Contains(t, info.Desc, "低风险首次修复")
	require.NotContains(t, info.Desc, "internal_cause")
	schemaValue, err := info.ParamsOneOf.ToJSONSchema()
	require.NoError(t, err)
	require.Contains(t, schemaValue.Required, "provider_id")
	require.Contains(t, schemaValue.Required, "expected_pool_generation")
	require.Contains(t, schemaValue.Required, "idempotency_key")
	require.NotContains(t, schemaValue.Required, "dry_run")
	generationSchema, exists := schemaValue.Properties.Get("expected_pool_generation")
	require.True(t, exists)
	require.Equal(t, "integer", generationSchema.Type)

	_, err = remediation.InvokableRun(context.Background(), `{
		"provider_id":"provider-search-a",
		"expected_pool_generation":12,
		"idempotency_key":"refresh-with-hidden-input",
		"internal_cause":"should-not-be-accepted"
	}`)
	require.ErrorContains(t, err, "is not allowed")
}

func TestToolsQueryAndExecuteSimulator(t *testing.T) {
	instance, item := newTestSimulator(t, "mapping-regression-rollback-001")
	ctx := context.Background()
	require.NoError(t, instance.Advance(ctx, 5*time.Minute))
	set, err := New(ctx, instance, item.Scenario.Metadata.ID)
	require.NoError(t, err)

	queryLogs, ok := set.Resolve("query_logs")
	require.True(t, ok)
	encodedLogs, err := queryLogs.InvokableRun(ctx, `{}`)
	require.NoError(t, err)
	var logsResponse response[[]platform.LogEntry]
	require.NoError(t, json.Unmarshal([]byte(encodedLogs), &logsResponse))
	require.Empty(t, logsResponse.Error)
	require.Len(t, logsResponse.Data, 1)
	require.Equal(t, "invalid_parameter_type", logsResponse.Data[0].Code)

	rollback, ok := set.Resolve("rollback_mapping")
	require.True(t, ok)
	encodedOperation, err := rollback.InvokableRun(ctx, `{
        "tool_id":"generate_image",
        "target_version":"mapping-v1",
        "expected_version":"mapping-v2",
        "idempotency_key":"agent-rollback-001"
    }`)
	require.NoError(t, err)
	var operationResponse response[platform.Operation]
	require.NoError(t, json.Unmarshal([]byte(encodedOperation), &operationResponse))
	require.Empty(t, operationResponse.Error)
	require.Equal(t, platform.OperationPending, operationResponse.Data.Status)

	require.NoError(t, instance.Advance(ctx, time.Minute))
	getOperation, ok := set.Resolve("get_operation")
	require.True(t, ok)
	encodedOperation, err = getOperation.InvokableRun(ctx, `{"operation_id":"`+operationResponse.Data.ID+`"}`)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(encodedOperation), &operationResponse))
	require.Equal(t, platform.OperationSucceeded, operationResponse.Data.Status)
}

func TestNewRejectsWrongIncident(t *testing.T) {
	instance, _ := newTestSimulator(t, "mapping-regression-rollback-001")
	set, err := New(context.Background(), instance, "different-incident")
	require.NoError(t, err)
	require.NotNil(t, set)
	_, visible := set.Resolve("rollback_mapping")
	require.False(t, visible)
}

func TestWorkflowToolsExposeOnlyAllowedAgentActions(t *testing.T) {
	instance, item := newTestSimulator(t, "mapping-regression-rollback-001")
	ctx := context.Background()
	flow, err := workflow.NewIncidentWorkflow(workflow.Config{IncidentID: item.Scenario.Metadata.ID})
	require.NoError(t, err)
	_, err = flow.Apply(workflow.Event{Type: workflow.EventStartInvestigation, Actor: workflow.ActorController})
	require.NoError(t, err)

	set, err := New(ctx, instance, item.Scenario.Metadata.ID, WithWorkflow(flow))
	require.NoError(t, err)
	visible := namesOfTools(t, set.ToolsForActions(flow.AllowedAgentActions()))
	require.Contains(t, visible, "query_metrics")
	require.Contains(t, visible, "get_services")
	require.Contains(t, visible, "get_remediation_capabilities")
	require.Contains(t, visible, "submit_plan")
	require.Contains(t, visible, "escalate_incident")
	require.NotContains(t, visible, "rollback_mapping")
	require.NotContains(t, visible, "request_probe")
	require.NotContains(t, visible, "request_recovery")

	_, err = flow.SubmitPlan(workflow.PlanDraft{
		Summary: "回滚 Mapping", RootCause: "mapping regression", EvidenceRefs: []string{"change:mapping-v2"},
		Remediation:  workflow.PlannedAction{ToolName: "rollback_mapping"},
		ProbeRouteID: "route-a", RecoveryPolicyID: "default-safe-recovery",
	})
	require.NoError(t, err)
	plannedVisible := namesOfTools(t, set.ToolsForActions(flow.AllowedAgentActions()))
	require.NotContains(t, plannedVisible, "submit_plan")
	require.Contains(t, plannedVisible, "query_metrics")
	require.Contains(t, plannedVisible, "escalate_incident")
}

func TestSubmitPlanToolAdvancesWorkflow(t *testing.T) {
	instance, item := newTestSimulator(t, "mapping-regression-rollback-001")
	ctx := context.Background()
	flow, err := workflow.NewIncidentWorkflow(workflow.Config{IncidentID: item.Scenario.Metadata.ID})
	require.NoError(t, err)
	_, err = flow.Apply(workflow.Event{Type: workflow.EventStartInvestigation, Actor: workflow.ActorController})
	require.NoError(t, err)
	set, err := New(ctx, instance, item.Scenario.Metadata.ID, WithWorkflow(flow))
	require.NoError(t, err)

	submit, ok := set.Resolve("submit_plan")
	require.True(t, ok)
	encoded, err := submit.InvokableRun(ctx, `{
		"summary":"回滚 Mapping 配置",
		"root_cause":"mapping schema regression",
		"evidence_refs":["log:invalid_parameter_type","change:mapping-v2"],
		"remediation_tool":"rollback_mapping",
		"remediation_arguments":{
			"tool_id":"generate_image",
			"target_version":"mapping-v1",
			"expected_version":"mapping-v2",
			"idempotency_key":"plan-rollback-001"
		},
		"probe_route_id":"route-a",
		"recovery_policy_id":"default-safe-recovery"
	}`)
	require.NoError(t, err)
	var result response[workflow.PlanSubmission]
	require.NoError(t, json.Unmarshal([]byte(encoded), &result))
	require.Empty(t, result.Error)
	assertSnapshot := flow.Snapshot()
	require.Equal(t, workflow.StatePlanned, assertSnapshot.State)
	require.NotNil(t, assertSnapshot.Plan)
	require.Equal(t, "rollback_mapping", assertSnapshot.Plan.Remediation.ToolName)
}

func toolNames(t *testing.T, set *Set) []string {
	t.Helper()
	return namesOfTools(t, set.Tools())
}

func namesOfTools(t *testing.T, tools []einotool.BaseTool) []string {
	t.Helper()
	result := make([]string, 0, len(tools))
	for _, item := range tools {
		info, err := item.Info(context.Background())
		require.NoError(t, err)
		result = append(result, info.Name)
	}
	sort.Strings(result)
	return result
}

func newTestSimulator(t *testing.T, id string) (*simulator.Simulator, *scenario.Case) {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", "data", "toolops-v1"))
	dataset, err := scenario.LoadDataset(root)
	require.NoError(t, err)
	item, ok := dataset.Find(id)
	require.True(t, ok)
	instance, err := simulator.New(item.Scenario)
	require.NoError(t, err)
	return instance, item
}
