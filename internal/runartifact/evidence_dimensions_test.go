package runartifact

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/workflow"
)

func dimensionKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	return result
}

func TestEvidenceDimensionCoverageClassifiesToolFamilies(t *testing.T) {
	runs := []AgentRun{{ToolCalls: []ToolCall{
		{Name: "get_services", Action: workflow.AgentActionRead, Status: "succeeded", CallID: "c1", EvidenceRef: "tool:get_services:a"},
		{Name: "query_metrics", Action: workflow.AgentActionRead, Status: "succeeded", CallID: "c2", EvidenceRef: "tool:query_metrics:b"},
		{Name: "query_traces", Action: workflow.AgentActionRead, Status: "failed", CallID: "c3"},
		{Name: "query_logs", Action: workflow.AgentActionSubmitExecutionIntent, Status: "succeeded", CallID: "c4"},
		{Name: "get_providers", Action: workflow.AgentActionRead, Status: "succeeded", CallID: "c5", EvidenceRef: "tool:get_providers:e"},
	}}}
	consulted, cited := EvidenceDimensionCoverage(runs, []string{"c2", "tool:get_providers:e", "  "})
	// get_services 不计入维度；失败的 trace 与写动作的日志同样不计。
	assert.ElementsMatch(t, []string{DimensionMetrics, DimensionConfigState}, dimensionKeys(consulted))
	assert.ElementsMatch(t, []string{DimensionMetrics, DimensionConfigState}, dimensionKeys(cited))
}

func TestEvidenceDimensionCoverageAcceptsCallIDAndEvidenceRef(t *testing.T) {
	runs := []AgentRun{{ToolCalls: []ToolCall{
		{Name: "query_traces", Action: workflow.AgentActionRead, Status: "succeeded", CallID: "c-trace", EvidenceRef: "tool:query_traces:t"},
	}}}
	_, byCallID := EvidenceDimensionCoverage(runs, []string{"c-trace"})
	assert.Contains(t, byCallID, DimensionTrace)
	_, byEvidenceRef := EvidenceDimensionCoverage(runs, []string{"tool:query_traces:t"})
	assert.Contains(t, byEvidenceRef, DimensionTrace)
}

func newEvidenceGateRecorder(t *testing.T) *Recorder {
	t.Helper()
	recorder := New("gate-scenario", Provenance{CodeVersion: "test", DatasetVersion: "toolops-v1"}, RunConfig{})
	recorder.BeginAgentRun("investigate", workflow.Snapshot{State: workflow.StateInvestigating})
	record := func(callID, name string) {
		recorder.RecordToolCall(callID, name, workflow.AgentActionRead, "{}", "{}", time.Now(), nil, false)
	}
	record("call-metrics", "query_metrics")
	record("call-logs", "query_logs")
	record("call-traces", "query_traces")
	record("call-config", "get_change_records")
	return recorder
}

func TestValidateIntentEvidenceRequiresAllDimensions(t *testing.T) {
	recorder := newEvidenceGateRecorder(t)
	// 四类维度齐备时放行。
	require.NoError(t, recorder.ValidateIntentEvidence([]string{"call-metrics", "call-logs", "call-traces", "call-config"}))
	// 缺 Trace 维度：错误必须点名缺失维度并给出处置路径。
	err := recorder.ValidateIntentEvidence([]string{"call-metrics", "call-logs", "call-config"})
	require.ErrorContains(t, err, "链路")
	require.ErrorContains(t, err, "critical_telemetry_missing")
	// 编造引用直接拒绝，不进入维度判断。
	err = recorder.ValidateIntentEvidence([]string{"call-metrics", "call-logs", "call-traces", "call-config", "fabricated-ref"})
	require.ErrorContains(t, err, "does not identify a successful read tool call")
}

func TestValidateIntentEvidenceRejectsWhenNoEvidenceAtAll(t *testing.T) {
	recorder := newEvidenceGateRecorder(t)
	err := recorder.ValidateIntentEvidence(nil)
	require.ErrorContains(t, err, "指标")
	require.ErrorContains(t, err, "配置状态")
}

func TestValidateEscalationEvidenceRequiresEveryConsultedDimension(t *testing.T) {
	recorder := newEvidenceGateRecorder(t)
	// 只交接部分维度：调查已获得的其余维度必须点名补齐。
	err := recorder.ValidateEscalationEvidence(platform.EscalationReasonCriticalTelemetryMissing, []string{"call-logs"}, nil)
	require.ErrorContains(t, err, "指标")
	require.ErrorContains(t, err, "链路")
	require.ErrorContains(t, err, "配置状态")
	require.NoError(t, recorder.ValidateEscalationEvidence(platform.EscalationReasonCriticalTelemetryMissing,
		[]string{"call-metrics", "call-logs", "call-traces", "call-config"}, nil))
}

func TestValidateEscalationEvidenceAllowsIncompleteDimensions(t *testing.T) {
	// 只调查到两个维度时，升级不要求补齐另外两类——查不齐正是升级的理由。
	recorder := New("gate-scenario", Provenance{CodeVersion: "test", DatasetVersion: "toolops-v1"}, RunConfig{})
	recorder.BeginAgentRun("investigate", workflow.Snapshot{State: workflow.StateInvestigating})
	recorder.RecordToolCall("call-logs", "query_logs", workflow.AgentActionRead, "{}", "{}", time.Now(), nil, false)
	require.NoError(t, recorder.ValidateEscalationEvidence(platform.EscalationReasonCriticalTelemetryMissing, []string{"call-logs"}, nil))
}

func recordEscalationGateActions(t *testing.T, recorder *Recorder, resolved ...workflow.ResolvedAction) {
	t.Helper()
	// 模拟 Controller 的 Checkpoint 回调：ResolvedActions 在运行中实时同步，
	// 门禁据此判定"尝试过探测/修复"（含随后被平台拒绝的尝试）。
	recorder.RecordWorkflowCheckpoint(workflow.Snapshot{ResolvedActions: resolved})
}

func TestValidateEscalationEvidenceRequiresProbeBeforeNoSafeClaim(t *testing.T) {
	recorder := newEvidenceGateRecorder(t)
	err := recorder.ValidateEscalationEvidence(platform.EscalationReasonNoSafeRemediationAvailable,
		[]string{"call-metrics", "call-logs", "call-traces", "call-config"}, []string{"rollback_mapping"})
	// Gate A：从未探测就断言无路可走会被拒绝，错误指明 request_probe 出口。
	require.ErrorContains(t, err, "request_probe")
	require.ErrorContains(t, err, "从未尝试过探测")

	recordEscalationGateActions(t, recorder,
		workflow.ResolvedAction{Kind: workflow.ActionKindProbe, ToolName: "request_probe"})
	require.NoError(t, recorder.ValidateEscalationEvidence(platform.EscalationReasonNoSafeRemediationAvailable,
		[]string{"call-metrics", "call-logs", "call-traces", "call-config"}, []string{"rollback_mapping"}))
}

func TestValidateEscalationEvidenceSkipsProbeGateForEmptyCatalog(t *testing.T) {
	recorder := newEvidenceGateRecorder(t)
	// 空授权目录：无路可走由目录本身证明，不强制探测（凭据/配额类人工域）。
	err := recorder.ValidateEscalationEvidence(platform.EscalationReasonNoSafeRemediationAvailable,
		[]string{"call-metrics", "call-logs", "call-traces", "call-config"}, nil)
	require.NoError(t, err)
}

func TestValidateEscalationEvidenceRequiresHonestBudgetExhaustion(t *testing.T) {
	recorder := newEvidenceGateRecorder(t)
	recordEscalationGateActions(t, recorder,
		workflow.ResolvedAction{Kind: workflow.ActionKindProbe, ToolName: "request_probe"},
		workflow.ResolvedAction{Kind: workflow.ActionKindRemediation, ToolName: "refresh_provider_connection"},
		workflow.ResolvedAction{Kind: workflow.ActionKindRemediation, ToolName: "recreate_provider_connection_pool"},
	)
	authorized := []string{"refresh_provider_connection", "recreate_provider_connection_pool"}
	// Gate B：授权动作全部尝试过还谎报"无安全修复手段"会被拒绝。
	err := recorder.ValidateEscalationEvidence(platform.EscalationReasonNoSafeRemediationAvailable,
		[]string{"call-metrics", "call-logs", "call-traces", "call-config"}, authorized)
	require.ErrorContains(t, err, "workflow_budget_exhausted")
	require.ErrorContains(t, err, "自治修复轮次已耗尽")
	// 如实申报预算耗尽即可通过。
	require.NoError(t, recorder.ValidateEscalationEvidence(platform.EscalationReasonWorkflowBudgetExhausted,
		[]string{"call-metrics", "call-logs", "call-traces", "call-config"}, authorized))
}

func TestValidateEscalationEvidenceBudgetGateAllowsUnattemptedTools(t *testing.T) {
	recorder := newEvidenceGateRecorder(t)
	recordEscalationGateActions(t, recorder,
		workflow.ResolvedAction{Kind: workflow.ActionKindProbe, ToolName: "request_probe"},
		workflow.ResolvedAction{Kind: workflow.ActionKindRemediation, ToolName: "refresh_provider_connection"},
	)
	// 还有一个授权动作没试过：不是预算耗尽，no_safe_remediation_available 可用。
	err := recorder.ValidateEscalationEvidence(platform.EscalationReasonNoSafeRemediationAvailable,
		[]string{"call-metrics", "call-logs", "call-traces", "call-config"},
		[]string{"refresh_provider_connection", "recreate_provider_connection_pool"})
	require.NoError(t, err)
}
