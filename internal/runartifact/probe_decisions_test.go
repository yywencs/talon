package runartifact

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/runmeta"
	"github.com/wen/opentalon/internal/workflow"
)

// compoundDraft 复现问题 22 的现场形态：目录含两个授权修复，草案只包含
// 第一个（rollback_mapping）与 probe Stage——复合故障第一周期的典型提交。
func compoundDraft(defaultDecision workflow.CheckpointDecision, rules ...workflow.CheckpointRule) []workflow.ExecutionStageDraft {
	return []workflow.ExecutionStageDraft{
		{StageID: "rollback", Actions: []workflow.IntendedAction{
			{Kind: workflow.ActionKindRemediation, ToolName: "rollback_mapping", Arguments: map[string]any{"idempotency_key": "k1"}},
		}, CheckpointPolicy: workflow.CheckpointPolicy{DefaultDecision: workflow.CheckpointContinue}},
		{StageID: "verify", Actions: []workflow.IntendedAction{
			{Kind: workflow.ActionKindProbe, ToolName: "request_probe", Arguments: map[string]any{"idempotency_key": "k2"}},
		}, CheckpointPolicy: workflow.CheckpointPolicy{DefaultDecision: defaultDecision, Rules: rules}},
	}
}

func TestRemainingAuthorizedRemediationsSubtractsAttemptedAndDraft(t *testing.T) {
	resolved := []workflow.ResolvedAction{
		{Kind: workflow.ActionKindRemediation, ToolName: "rollback_mapping"},
		{Kind: workflow.ActionKindProbe, ToolName: "request_probe"},
	}
	draft := []workflow.ExecutionStageDraft{{StageID: "recreate", Actions: []workflow.IntendedAction{
		{Kind: workflow.ActionKindRemediation, ToolName: "recreate_provider_connection_pool"},
	}}}
	// 既往尝试与草案自带动作都被扣除；输出保持传入顺序并去重。
	assert.Equal(t, []string{"switch_provider_route"},
		RemainingAuthorizedRemediations(resolved, draft,
			[]string{"rollback_mapping", "recreate_provider_connection_pool", "switch_provider_route", "switch_provider_route"}))
	// 全部覆盖时返回空切片，便于直接做 len 判定。
	assert.Empty(t, RemainingAuthorizedRemediations(resolved, draft,
		[]string{"rollback_mapping", "recreate_provider_connection_pool"}))
}

func TestGateProbeCheckpointDecisionsRejectsTerminalDefaultWithRemainingCapabilities(t *testing.T) {
	authorized := []string{"rollback_mapping", "recreate_provider_connection_pool"}
	err := GateProbeCheckpointDecisions(nil, compoundDraft(workflow.CheckpointFailed), authorized, "")
	require.Error(t, err)
	// 拒绝消息必须点名剩余动作、正确去向（needs_agent）与升级出口。
	require.ErrorContains(t, err, "recreate_provider_connection_pool")
	require.ErrorContains(t, err, "needs_agent")
	require.ErrorContains(t, err, "escalate_incident")
	require.ErrorContains(t, err, "verify")
}

func TestGateProbeCheckpointDecisionsAllowsNeedsAgentDefault(t *testing.T) {
	authorized := []string{"rollback_mapping", "recreate_provider_connection_pool"}
	require.NoError(t, GateProbeCheckpointDecisions(nil, compoundDraft(workflow.CheckpointNeedsAgent), authorized, ""))
}

func TestGateProbeCheckpointDecisionsRejectsTerminalRules(t *testing.T) {
	authorized := []string{"rollback_mapping", "recreate_provider_connection_pool"}
	rules := []workflow.CheckpointRule{
		{SourceActionID: "verify-action", OutputPath: "output.outcome", Equals: "healthy", Decision: workflow.CheckpointContinue},
		{SourceActionID: "verify-action", OutputPath: "output.outcome", Equals: "hard_stop", Decision: workflow.CheckpointEscalate},
	}
	err := GateProbeCheckpointDecisions(nil, compoundDraft(workflow.CheckpointNeedsAgent, rules...), authorized, "")
	require.Error(t, err)
	require.ErrorContains(t, err, "rules[1]")
	require.ErrorContains(t, err, "escalate")
}

func TestGateProbeCheckpointDecisionsAllowsTerminalWhenCatalogExhausted(t *testing.T) {
	// 草案已包含全部授权修复：探测失败时确无剩余自治路径，终局决策合法。
	authorized := []string{"rollback_mapping", "recreate_provider_connection_pool"}
	require.NoError(t, GateProbeCheckpointDecisions(nil, compoundDraft(workflow.CheckpointFailed), authorized[:1], ""))
	// 既往已尝试第二个动作、草案不再包含它时同样视为耗尽。
	resolved := []workflow.ResolvedAction{
		{Kind: workflow.ActionKindRemediation, ToolName: "recreate_provider_connection_pool"},
	}
	require.NoError(t, GateProbeCheckpointDecisions(resolved, compoundDraft(workflow.CheckpointEscalate), authorized, ""))
	// 空授权目录（凭据/配额类人工域）不受限。
	require.NoError(t, GateProbeCheckpointDecisions(nil, compoundDraft(workflow.CheckpointFailed), nil, ""))
}

func TestGateProbeCheckpointDecisionsIgnoresNonProbeStages(t *testing.T) {
	// 非 probe Stage 的终局默认决策不受本门禁约束（由各自 Stage 门禁负责）。
	draft := []workflow.ExecutionStageDraft{
		{StageID: "recover", Actions: []workflow.IntendedAction{
			{Kind: workflow.ActionKindRecovery, ToolName: "request_recovery"},
		}, CheckpointPolicy: workflow.CheckpointPolicy{DefaultDecision: workflow.CheckpointFailed}},
	}
	require.NoError(t, GateProbeCheckpointDecisions(nil, draft, []string{"rollback_mapping"}, ""))
}

func TestGateProbeCheckpointDecisionsDetectsProbeByToolName(t *testing.T) {
	// 与 Workflow 提交校验同口径：未标注 kind 的 request_probe 也按 probe 处理。
	draft := []workflow.ExecutionStageDraft{{StageID: "verify", Actions: []workflow.IntendedAction{
		{ToolName: "request_probe"},
	}, CheckpointPolicy: workflow.CheckpointPolicy{DefaultDecision: workflow.CheckpointBlocked}}}
	err := GateProbeCheckpointDecisions(nil, draft, []string{"rollback_mapping"}, "")
	require.Error(t, err)
	require.ErrorContains(t, err, "blocked")
}

func TestValidateIntentProbeDecisionsReadsResolvedActions(t *testing.T) {
	recorder := New("gate-scenario", runmeta.Provenance{CodeVersion: "test", DatasetVersion: "toolops-v1"}, runmeta.Config{})
	recorder.BeginAgentRun("investigate", workflow.Snapshot{State: workflow.StateInvestigating})
	authorized := []string{"rollback_mapping", "recreate_provider_connection_pool"}
	// 第一周期提交：recreate 未尝试也不在草案内，failed 默认决策被拒。
	err := recorder.ValidateIntentProbeDecisions(compoundDraft(workflow.CheckpointFailed), authorized, "")
	require.ErrorContains(t, err, "recreate_provider_connection_pool")
	// 第一周期执行后（rollback 已尝试），第二周期草案含 recreate：目录耗尽，放行。
	recordEscalationGateActions(t, recorder,
		workflow.ResolvedAction{Kind: workflow.ActionKindRemediation, ToolName: "rollback_mapping"})
	secondCycle := []workflow.ExecutionStageDraft{
		{StageID: "recreate", Actions: []workflow.IntendedAction{
			{Kind: workflow.ActionKindRemediation, ToolName: "recreate_provider_connection_pool", Arguments: map[string]any{"idempotency_key": "k1"}},
		}, CheckpointPolicy: workflow.CheckpointPolicy{DefaultDecision: workflow.CheckpointContinue}},
		{StageID: "verify", Actions: []workflow.IntendedAction{
			{Kind: workflow.ActionKindProbe, ToolName: "request_probe", Arguments: map[string]any{"idempotency_key": "k2"}},
		}, CheckpointPolicy: workflow.CheckpointPolicy{DefaultDecision: workflow.CheckpointFailed}},
	}
	require.NoError(t, recorder.ValidateIntentProbeDecisions(secondCycle, authorized, ""))
}

func TestGateProbeCheckpointDecisionsRequiredPolicyForcesNeedsAgent(t *testing.T) {
	// 探测适用性 required 的场景（如瞬时自愈）：即使授权目录为空，
	// 探测不健康也必须以 needs_agent 交回 Agent，不得终局。
	draft := []workflow.ExecutionStageDraft{
		{StageID: "verify", Actions: []workflow.IntendedAction{
			{Kind: workflow.ActionKindProbe, ToolName: "request_probe", Arguments: map[string]any{"idempotency_key": "k1"}},
		}, CheckpointPolicy: workflow.CheckpointPolicy{DefaultDecision: workflow.CheckpointEscalate}},
	}
	err := GateProbeCheckpointDecisions(nil, draft, nil, platform.ProbeEscalationRequired)
	require.ErrorContains(t, err, "探测适用性为 required")
	require.ErrorContains(t, err, "needs_agent")

	draft[0].CheckpointPolicy.DefaultDecision = workflow.CheckpointNeedsAgent
	require.NoError(t, GateProbeCheckpointDecisions(nil, draft, nil, platform.ProbeEscalationRequired))

	// not_applicable/conditional 保持原行为：空目录放行终局决策。
	draft[0].CheckpointPolicy.DefaultDecision = workflow.CheckpointEscalate
	require.NoError(t, GateProbeCheckpointDecisions(nil, draft, nil, platform.ProbeEscalationNotApplicable))
	require.NoError(t, GateProbeCheckpointDecisions(nil, draft, nil, ""))
}
