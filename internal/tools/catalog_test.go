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
	"github.com/wen/opentalon/internal/skill"
	"github.com/wen/opentalon/internal/workflow"
)

func TestSetBuildsIncidentScopedEinoTools(t *testing.T) {
	instance, item := newTestSimulator(t, "mapping-regression-rollback-001")
	set, err := New(context.Background(), instance, item.Scenario.Metadata.ID)
	require.NoError(t, err)

	names := toolNames(t, set)
	require.Contains(t, names, "query_metrics")
	require.Contains(t, names, "query_logs")
	require.Contains(t, names, "get_evidence")
	require.Contains(t, names, "get_recovery_policies")
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

	getPolicies, ok := set.Resolve("get_recovery_policies")
	require.True(t, ok)
	encodedPolicies, err := getPolicies.InvokableRun(ctx, `{}`)
	require.NoError(t, err)
	var policiesResponse response[[]platform.RecoveryPolicy]
	require.NoError(t, json.Unmarshal([]byte(encodedPolicies), &policiesResponse))
	require.Empty(t, policiesResponse.Error)
	require.Len(t, policiesResponse.Data, 1)
	require.Equal(t, "default-safe-recovery", policiesResponse.Data[0].ID)
	require.Equal(t, []float64{0.01, 0.05}, policiesResponse.Data[0].ProbeSteps)
	require.Equal(t, 3, policiesResponse.Data[0].HealthyWindowsRequired)

	queryLogs, ok := set.Resolve("query_logs")
	require.True(t, ok)
	encodedLogs, err := queryLogs.InvokableRun(ctx, `{}`)
	require.NoError(t, err)
	var logsResponse response[[]platform.LogEntry]
	require.NoError(t, json.Unmarshal([]byte(encodedLogs), &logsResponse))
	require.Empty(t, logsResponse.Error)
	require.Len(t, logsResponse.Data, 1)
	require.Equal(t, "invalid_parameter_type", logsResponse.Data[0].Code)
	require.Equal(t, []string{"log.invalid_parameter_type"}, logsResponse.EvidenceIDs)

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
	require.Contains(t, visible, "get_evidence")
	require.Contains(t, visible, "get_services")
	require.Contains(t, visible, "get_remediation_capabilities")
	require.Contains(t, visible, "get_recovery_policies")
	require.Contains(t, visible, "submit_execution_intent")
	require.Contains(t, visible, "escalate_incident")
	require.NotContains(t, visible, "rollback_mapping")
	require.NotContains(t, visible, "request_probe")
	require.NotContains(t, visible, "request_recovery")

	_, err = flow.SubmitExecutionIntent(workflow.ExecutionIntentDraft{
		Summary: "回滚 Mapping", RootCause: "mapping regression", EvidenceRefs: []string{"change:mapping-v2"},
		Stages: []workflow.ExecutionStageDraft{{StageID: "rollback", Goal: "rollback mapping",
			Actions: []workflow.IntendedAction{{Key: "rollback-mapping", ToolName: "rollback_mapping"}}}},
	})
	require.NoError(t, err)
	validatingVisible := namesOfTools(t, set.ToolsForActions(flow.AllowedAgentActions()))
	require.NotContains(t, validatingVisible, "submit_execution_intent")
	require.Contains(t, validatingVisible, "query_metrics")
	require.Contains(t, validatingVisible, "escalate_incident")
}

func TestSkillToolsIntersectWorkflowAndSkillWhitelist(t *testing.T) {
	instance, item := newTestSimulator(t, "mapping-regression-rollback-001")
	ctx := context.Background()
	flow, err := workflow.NewIncidentWorkflow(workflow.Config{IncidentID: item.Scenario.Metadata.ID})
	require.NoError(t, err)
	_, err = flow.Apply(workflow.Event{Type: workflow.EventStartInvestigation, Actor: workflow.ActorController})
	require.NoError(t, err)
	registry, err := skill.LoadDirectory("../../skills")
	require.NoError(t, err)
	session, err := skill.NewSession(registry, 2, nil)
	require.NoError(t, err)

	set, err := New(ctx, instance, item.Scenario.Metadata.ID, WithWorkflow(flow), WithSkillSession(session))
	require.NoError(t, err)
	policy := AgentToolNamesForSkills(nil, false)
	visibleTools, err := set.ToolsForActionsAndNames(flow.AllowedAgentActions(), policy)
	require.NoError(t, err)
	visible := namesOfTools(t, visibleTools)
	require.Contains(t, visible, "query_metrics")
	require.Contains(t, visible, "query_logs")
	require.Contains(t, visible, "query_traces")
	require.Contains(t, visible, "get_evidence")
	require.Contains(t, visible, "load_skill")
	require.NotContains(t, visible, "submit_execution_intent")
	require.NotContains(t, visible, "get_change_records")

	_, err = session.Activate("mapping-diagnosis", "mapping evidence", []string{"call-query-logs"})
	require.NoError(t, err)
	active := session.Active()
	policy = AgentToolNamesForSkills(active[0].AllowedTools, true)
	visibleTools, err = set.ToolsForActionsAndNames(flow.AllowedAgentActions(), policy)
	require.NoError(t, err)
	visible = namesOfTools(t, visibleTools)
	require.Contains(t, visible, "submit_execution_intent")
	require.Contains(t, visible, "get_change_records")
	require.Contains(t, visible, "unload_skill")
	require.NotContains(t, visible, "get_credential_metadata")
	require.NotContains(t, visible, "get_connection_metadata")
	require.NotContains(t, visible, "get_tasks")
	_, err = session.Activate("credential-diagnosis", "authentication evidence", []string{"call-query-logs"})
	require.NoError(t, err)
	active = session.Active()
	combined := append(append([]string(nil), active[0].AllowedTools...), active[1].AllowedTools...)
	policy = AgentToolNamesForSkills(combined, true)
	visibleTools, err = set.ToolsForActionsAndNames(flow.AllowedAgentActions(), policy)
	require.NoError(t, err)
	visible = namesOfTools(t, visibleTools)
	require.Contains(t, visible, "get_change_records")
	require.Contains(t, visible, "get_credential_metadata")
	require.Contains(t, visible, "query_traces")
	require.NotContains(t, visible, "get_connection_metadata")

	_, err = set.ToolsForActionsAndNames(flow.AllowedAgentActions(), []string{"unknown_tool"})
	require.ErrorContains(t, err, "is not registered")
	_, err = set.ToolsForActionsAndNames(flow.AllowedAgentActions(), []string{"rollback_mapping"})
	require.ErrorContains(t, err, "not available to the Agent workflow")
}

func TestAgentToolNamesKeepTraceQueryVisibleForCredentialSkill(t *testing.T) {
	visible := AgentToolNamesForSkills([]string{"query_logs", "get_credential_metadata"}, true)
	require.Contains(t, visible, "query_traces")
	require.Equal(t, 1, countToolName(visible, "query_traces"))
}

func countToolName(values []string, expected string) int {
	count := 0
	for _, value := range values {
		if value == expected {
			count++
		}
	}
	return count
}

func TestSubmitExecutionIntentToolAdvancesWorkflow(t *testing.T) {
	instance, item := newTestSimulator(t, "mapping-regression-rollback-001")
	ctx := context.Background()
	flow, err := workflow.NewIncidentWorkflow(workflow.Config{IncidentID: item.Scenario.Metadata.ID})
	require.NoError(t, err)
	_, err = flow.Apply(workflow.Event{Type: workflow.EventStartInvestigation, Actor: workflow.ActorController})
	require.NoError(t, err)
	set, err := New(ctx, instance, item.Scenario.Metadata.ID, WithWorkflow(flow))
	require.NoError(t, err)

	submit, ok := set.Resolve("submit_execution_intent")
	require.True(t, ok)
	invalid, err := submit.InvokableRun(ctx, `{
		"summary":"回滚 Mapping 配置",
		"root_cause":"mapping schema regression",
		"evidence_refs":["log:invalid_parameter_type","change:mapping-v2"],
		"actions":[{"tool_name":"rollback_mapping","arguments":{
			"tool_id":"generate_image",
			"target_version":"mapping-v1",
			"expected_version":"mapping-v2",
			"idempotency_key":"intent-invalid-policy-001"
		}}],
		"probe_route_id":"route-a",
		"recovery_policy_id":"invented-policy"
	}`)
	require.NoError(t, err)
	var invalidResult response[workflow.ExecutionIntentSubmission]
	require.NoError(t, json.Unmarshal([]byte(invalid), &invalidResult))
	require.Contains(t, invalidResult.Error, "intent stages is required")
	require.Equal(t, workflow.StateInvestigating, flow.Snapshot().State)

	conflicting, err := submit.InvokableRun(ctx, `{
		"summary":"验证探测参数",
		"root_cause":"confirmed",
		"evidence_refs":["trace:probe"],
		"stages":[{"stage_id":"probe","goal":"verify","actions":[{"id":"probe-route","tool_name":"request_probe","arguments":{
			"route_id":"route-a","policy_id":"default-safe-recovery","recovery_policy_id":"default-safe-recovery","idempotency_key":"probe-conflict"
		}}]}]
	}`)
	require.NoError(t, err, "invalid intent input must be returned as a tool result so ReAct can correct it")
	var conflictingResult response[workflow.ExecutionIntentSubmission]
	require.NoError(t, json.Unmarshal([]byte(conflicting), &conflictingResult))
	require.Contains(t, conflictingResult.Error, "cannot contain both policy_id and recovery_policy_id")
	require.Equal(t, workflow.StateInvestigating, flow.Snapshot().State)

	wrongCheckpointType, err := submit.InvokableRun(ctx, `{
		"summary":"验证 Checkpoint 类型",
		"root_cause":"confirmed",
		"evidence_refs":["trace:checkpoint"],
		"stages":[{"stage_id":"rollback","goal":"rollback","actions":[{"id":"rollback-action","tool_name":"rollback_mapping","arguments":{
			"tool_id":"generate_image","target_version":"mapping-v1","expected_version":"mapping-v2","idempotency_key":"rollback-checkpoint-type"
		}}],"checkpoint_policy":{"rules":[{
			"source_action_id":"rollback-action","output_path":"operation_status","equals":true,"decision":"succeeded"
		}]}}]
	}`)
	require.NoError(t, err, "checkpoint type errors must be returned as a tool result so ReAct can correct them")
	var wrongCheckpointTypeResult response[workflow.ExecutionIntentSubmission]
	require.NoError(t, json.Unmarshal([]byte(wrongCheckpointType), &wrongCheckpointTypeResult))
	require.Contains(t, wrongCheckpointTypeResult.Error, "operation_status requires a non-empty string comparison value")
	require.Equal(t, workflow.StateInvestigating, flow.Snapshot().State)

	emptyProbeCheckpoint, err := submit.InvokableRun(ctx, `{
		"summary":"验证空 Probe Checkpoint",
		"root_cause":"confirmed",
		"evidence_refs":["trace:probe"],
		"stages":[
			{"stage_id":"probe","goal":"verify","actions":[{"id":"probe-route","tool_name":"request_probe","arguments":{
				"route_id":"route-a","policy_id":"default-safe-recovery","idempotency_key":"probe-empty-checkpoint"
			}}],"checkpoint_policy":{}},
			{"stage_id":"recover","goal":"recover","actions":[{"id":"recover-route","tool_name":"request_recovery","arguments":{
				"route_id":"route-a","policy_id":"default-safe-recovery","idempotency_key":"recover-after-probe"
			}}],"checkpoint_policy":{"default_decision":"succeeded"}}
		]
	}`)
	require.NoError(t, err, "unsafe probe checkpoint errors must be returned as a tool result so ReAct can correct them")
	var emptyProbeCheckpointResult response[workflow.ExecutionIntentSubmission]
	require.NoError(t, json.Unmarshal([]byte(emptyProbeCheckpoint), &emptyProbeCheckpointResult))
	require.Contains(t, emptyProbeCheckpointResult.Error, "explicit fail-closed default_decision")
	require.Equal(t, workflow.StateInvestigating, flow.Snapshot().State)

	directProbeSuccess, err := submit.InvokableRun(ctx, `{
		"summary":"验证 Probe 不可直接结束 Incident",
		"root_cause":"confirmed",
		"evidence_refs":["trace:probe"],
		"stages":[
			{"stage_id":"probe","goal":"verify","actions":[{"id":"probe-route","tool_name":"request_probe","arguments":{
				"route_id":"route-a","policy_id":"default-safe-recovery","idempotency_key":"probe-direct-success"
			}}],"checkpoint_policy":{"rules":[{
				"source_action_id":"probe-route","output_path":"output.outcome","equals":"healthy","decision":"succeeded"
			}],"default_decision":"needs_agent"}},
			{"stage_id":"recover","goal":"recover","actions":[{"id":"recover-route","tool_name":"request_recovery","arguments":{
				"route_id":"route-a","policy_id":"default-safe-recovery","idempotency_key":"recover-after-direct-success"
			}}],"checkpoint_policy":{"default_decision":"succeeded"}}
		]
	}`)
	require.NoError(t, err, "unsafe probe completion must be returned as a tool result so ReAct can correct it")
	var directProbeSuccessResult response[workflow.ExecutionIntentSubmission]
	require.NoError(t, json.Unmarshal([]byte(directProbeSuccess), &directProbeSuccessResult))
	require.Contains(t, directProbeSuccessResult.Error, "cannot select succeeded for a probe stage")
	require.Equal(t, workflow.StateInvestigating, flow.Snapshot().State)

	encoded, err := submit.InvokableRun(ctx, `{
		"summary":"回滚 Mapping 配置",
		"root_cause":"mapping schema regression",
		"evidence_refs":["log:invalid_parameter_type","change:mapping-v2"],
		"stages":[{"stage_id":"rollback","goal":"rollback mapping","actions":[{"id":"rollback-mapping","tool_name":"rollback_mapping","arguments":{
			"tool_id":"generate_image",
			"target_version":"mapping-v1",
			"expected_version":"mapping-v2",
			"idempotency_key":"intent-rollback-001"
		}}],"checkpoint_policy":{"default_decision":"continue"}},
		{"stage_id":"probe","goal":"verify repaired route","actions":[{"id":"probe-route","tool_name":"request_probe","arguments":{
			"route_id":"route-a","recovery_policy_id":"default-safe-recovery","idempotency_key":"intent-probe-001"
		}}],"checkpoint_policy":{"rules":[
			{"source_action_id":"probe-route","output_path":"output.outcome","equals":"healthy","decision":"continue","next_stage_id":"recovery"},
			{"source_action_id":"probe-route","output_path":"output.outcome","equals":"hard_stop","decision":"needs_agent"}
		],"default_decision":"needs_agent"}},
		{"stage_id":"recovery","goal":"restore baseline traffic","actions":[{"id":"recover-route","tool_name":"request_recovery","arguments":{
			"route_id":"route-a","recovery_policy_id":"default-safe-recovery","idempotency_key":"intent-recovery-001"
		}}],"checkpoint_policy":{"default_decision":"succeeded"}}]
	}`)
	require.NoError(t, err)
	var result response[workflow.ExecutionIntentSubmission]
	require.NoError(t, json.Unmarshal([]byte(encoded), &result))
	require.Empty(t, result.Error)
	assertSnapshot := flow.Snapshot()
	require.Equal(t, workflow.StateValidating, assertSnapshot.State)
	require.NotNil(t, assertSnapshot.ExecutionIntent)
	require.Len(t, assertSnapshot.ExecutionIntent.Stages, 3)
	require.Equal(t, "rollback_mapping", assertSnapshot.ExecutionIntent.Stages[0].Actions[0].ToolName)
	require.Equal(t, workflow.ActionKindProbe, assertSnapshot.ExecutionIntent.Stages[1].Actions[0].Kind)
	require.Equal(t, workflow.ActionKindRecovery, assertSnapshot.ExecutionIntent.Stages[2].Actions[0].Kind)
	require.Equal(t, "default-safe-recovery", assertSnapshot.ExecutionIntent.Stages[1].Actions[0].Arguments["policy_id"])
	require.NotContains(t, assertSnapshot.ExecutionIntent.Stages[1].Actions[0].Arguments, "recovery_policy_id")
	require.Equal(t, "default-safe-recovery", assertSnapshot.ExecutionIntent.Stages[2].Actions[0].Arguments["policy_id"])
	require.NotContains(t, assertSnapshot.ExecutionIntent.Stages[2].Actions[0].Arguments, "recovery_policy_id")
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
