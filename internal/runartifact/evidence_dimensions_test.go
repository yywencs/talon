package runartifact

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	err := recorder.ValidateEscalationEvidence([]string{"call-logs"})
	require.ErrorContains(t, err, "指标")
	require.ErrorContains(t, err, "链路")
	require.ErrorContains(t, err, "配置状态")
	require.NoError(t, recorder.ValidateEscalationEvidence([]string{"call-metrics", "call-logs", "call-traces", "call-config"}))
}

func TestValidateEscalationEvidenceAllowsIncompleteDimensions(t *testing.T) {
	// 只调查到两个维度时，升级不要求补齐另外两类——查不齐正是升级的理由。
	recorder := New("gate-scenario", Provenance{CodeVersion: "test", DatasetVersion: "toolops-v1"}, RunConfig{})
	recorder.BeginAgentRun("investigate", workflow.Snapshot{State: workflow.StateInvestigating})
	recorder.RecordToolCall("call-logs", "query_logs", workflow.AgentActionRead, "{}", "{}", time.Now(), nil, false)
	require.NoError(t, recorder.ValidateEscalationEvidence([]string{"call-logs"}))
}
