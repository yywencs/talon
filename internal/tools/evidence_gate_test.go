package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type stubEvidenceGate struct {
	intentErr     error
	escalationErr error
}

func (s stubEvidenceGate) ValidateIntentEvidence([]string) error     { return s.intentErr }
func (s stubEvidenceGate) ValidateEscalationEvidence([]string) error { return s.escalationErr }

func TestSubmitExecutionIntentGateRejectsAsCorrectableError(t *testing.T) {
	tool, err := newSubmitExecutionIntentTool(nil, nil, stubEvidenceGate{intentErr: errors.New("证据引用缺少维度：链路（query_traces）")})
	require.NoError(t, err)
	raw, runErr := tool.InvokableRun(context.Background(), `{"summary":"rollback","root_cause":"mapping regression","evidence_refs":["tool:query_logs:x"],"stages":[]}`)
	// 门禁拒绝是可纠正的工具结果：不返回 Go 错误，模型可在下一轮补齐后重试。
	require.NoError(t, runErr)
	var decoded struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &decoded))
	require.Contains(t, decoded.Error, "链路")
}

func TestSubmitExecutionIntentNilGateKeepsLegacyBehavior(t *testing.T) {
	tool, err := newSubmitExecutionIntentTool(nil, nil, nil)
	require.NoError(t, err)
	raw, runErr := tool.InvokableRun(context.Background(), `{"summary":"rollback","root_cause":"mapping regression","evidence_refs":[],"stages":[]}`)
	require.NoError(t, runErr)
	var decoded struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &decoded))
	// gate 缺席时不应改变既有校验顺序：先撞到 stages 必填校验。
	require.Contains(t, decoded.Error, "intent stages is required")
}

func TestEscalateIncidentGateRejectsBeforePlatformCall(t *testing.T) {
	instance, item := newTestSimulator(t, "credential-revoked-escalation-001")
	set, err := New(context.Background(), instance, item.Scenario.Metadata.ID,
		WithEvidenceGate(stubEvidenceGate{escalationErr: errors.New("升级引用未包含：指标（query_metrics）")}))
	require.NoError(t, err)
	escalation, ok := set.Resolve("escalate_incident")
	require.True(t, ok)
	raw, runErr := escalation.InvokableRun(context.Background(), `{
		"reason_code": "no_safe_remediation_available",
		"reason": "credential revoked and managed by platform-security",
		"evidence_refs": ["tool:query_logs:x"],
		"handoff": {
			"affected_service": "sync-service",
			"current_protection_state": {"routes": []},
			"recommended_human_action": "rotate credential"
		},
		"idempotency_key": "escalate-gate-test"
	}`)
	require.NoError(t, runErr)
	var decoded struct {
		Error string         `json:"error"`
		Data  map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &decoded))
	require.Contains(t, decoded.Error, "指标")
	// 平台未收到升级请求：data 是零值 Operation，没有任何执行状态。
	require.Equal(t, "", decoded.Data["status"])
}
