//go:build integration

package agent

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/config"
	"github.com/wen/opentalon/internal/llm"
	"github.com/wen/opentalon/internal/observability"
	"github.com/wen/opentalon/internal/workflow"
)

// TestToolOpsAgentWithRealLLM 显式验证真实模型、数据集、Simulator、工具层和 Workflow 的集成链路。
// 默认跳过，避免普通单元测试产生外部请求；只有设置 TALON_REAL_LLM=1 时才会调用配置中的 API。
func TestToolOpsAgentWithRealLLM(t *testing.T) {
	if os.Getenv("TALON_REAL_LLM") != "1" {
		t.Skip("set TALON_REAL_LLM=1 to run the external LLM integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	// 先加载项目 .env，使其中的 COZELOOP_* 配置也能被观测初始化读取。
	llmConfig, err := config.LoadLLMConfig()
	require.NoError(t, err)
	observabilityConfig := observability.LoadConfigFromEnv()
	require.NoError(t, observability.Init(ctx, observabilityConfig))
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		require.NoError(t, observability.Shutdown(shutdownCtx))
	})

	chatModel, err := llm.NewChatModel(ctx, llmConfig)
	require.NoError(t, err)

	service, incidentID := testSimulator(t)
	// 场景在 5m 时发布 mapping-v2 并触发异常；必须先推进到 Incident 时刻，
	// 否则 Agent 看到的仍是 mapping-v1 和健康指标，会产生没有证据的无效计划。
	require.NoError(t, service.Advance(ctx, 5*time.Minute))
	incidentSnapshot := service.Snapshot()
	require.True(t, incidentSnapshot.Configs["mapping-v2"].Active)
	require.Equal(t, 0.78, incidentSnapshot.Traffic.SuccessRate)
	require.NotEmpty(t, incidentSnapshot.Logs)
	require.NotEmpty(t, incidentSnapshot.Traces)
	require.NotEmpty(t, incidentSnapshot.Changes)

	flow := investigatingWorkflow(t, incidentID)
	toolOpsAgent, err := NewToolOpsAgent(ctx, Config{
		Model:      chatModel,
		Platform:   service,
		IncidentID: incidentID,
		Workflow:   flow,
		MaxSteps:   24,
	})
	require.NoError(t, err)

	result, err := toolOpsAgent.Run(ctx, "调查当前 Incident。读取足够的指标、日志、Trace 和变更证据，确认根因后必须调用 submit_plan 提交一个安全、可验证的修复计划；不要直接执行修复。")
	require.NoError(t, err)
	require.NotNil(t, result)
	t.Logf("agent response: %s", result.Content)

	snapshot := flow.Snapshot()
	t.Logf("workflow state: %s, transitions: %d", snapshot.State, len(snapshot.History))
	assert.Equal(t, workflow.StatePlanned, snapshot.State)
	require.NotNil(t, snapshot.Plan)
	assert.NotEmpty(t, snapshot.Plan.RootCause)
	assert.NotEmpty(t, snapshot.Plan.EvidenceRefs)
	require.NotEmpty(t, snapshot.Plan.Stages)
	require.NotEmpty(t, snapshot.Plan.Stages[0].Actions)
	assert.Equal(t, "rollback_mapping", snapshot.Plan.Stages[0].Actions[0].ToolName)
	assert.Equal(t, "mapping-v1", snapshot.Plan.Stages[0].Actions[0].Arguments["target_version"])
	assert.Equal(t, "mapping-v2", snapshot.Plan.Stages[0].Actions[0].Arguments["expected_version"])
}
