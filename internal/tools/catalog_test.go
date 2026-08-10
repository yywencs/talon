package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/scenario"
	"github.com/wen/opentalon/internal/simulator"
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

func toolNames(t *testing.T, set *Set) []string {
	t.Helper()
	result := make([]string, 0, len(set.Tools()))
	for _, item := range set.Tools() {
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
