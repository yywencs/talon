package runartifact

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/workflow"
)

func TestRunArtifactExportsVersionMetadataAndReadsLegacySchemaVersion(t *testing.T) {
	artifact := New("incident-001", Versions{
		AgentVersion: "agent/v3", DatasetVersion: "dataset/2026-08-14",
	}).Snapshot()
	payload, err := json.Marshal(artifact)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"artifact_schema_version":"talon.run-artifact/v2",
		"agent_version":"agent/v3",
		"dataset_version":"dataset/2026-08-14",
		"run_id":"`+artifact.RunID+`",
		"scenario_id":"incident-001",
		"started_at":"`+artifact.StartedAt.Format(time.RFC3339Nano)+`",
		"finished_at":"0001-01-01T00:00:00Z",
		"duration":0,
		"outcome":"running",
		"agent_runs":[],
		"plans":[],
		"workflow_history":[],
		"blocked_attempts":[],
		"summary":{"agent_runs":0,"model_calls":0,"tool_calls":0,"invalid_tool_calls":0,"blocked_attempts":0,"prompt_tokens":0,"completion_tokens":0,"total_tokens":0,"llm_duration":0}
	}`, string(payload))
	assert.NotContains(t, string(payload), `"schema_version"`)

	var legacy RunArtifact
	require.NoError(t, json.Unmarshal([]byte(`{"schema_version":"talon.run-artifact/v1","run_id":"legacy"}`), &legacy))
	assert.Equal(t, "talon.run-artifact/v1", legacy.ArtifactSchemaVersion)
}

func TestRecorderSummarizesModelsEvidenceAndBlockedCalls(t *testing.T) {
	recorder := New("incident-001", Versions{AgentVersion: "agent/v1", DatasetVersion: "dataset/v1"})
	recorder.BeginAgentRun("investigate", workflow.Snapshot{State: workflow.StateInvestigating})
	started := time.Now().Add(-time.Millisecond)
	recorder.RecordModelCall(started, &schema.Message{ResponseMeta: &schema.ResponseMeta{
		FinishReason: "tool_calls",
		Usage: &schema.TokenUsage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18,
			PromptTokenDetails:      schema.PromptTokenDetails{CachedTokens: 3},
			CompletionTokensDetails: schema.CompletionTokensDetails{ReasoningTokens: 2}},
	}}, nil)
	recorder.RecordToolCall("call-1", "query_logs", workflow.AgentActionRead, `{}`, `{"data":[{"code":"connection_refused"}]}`, started, nil, false)
	recorder.RecordToolCall("call-2", "submit_plan", workflow.AgentActionSubmitPlan, `{}`, `{"data":null,"error":"not allowed in state planned"}`, started, nil, true)
	recorder.EndAgentRun(workflow.Snapshot{State: workflow.StatePlanned}, nil)

	artifact := recorder.Finish("resolved", workflow.Snapshot{}, nil)
	require.Len(t, artifact.AgentRuns, 1)
	assert.Equal(t, 1, artifact.Summary.ModelCalls)
	assert.Equal(t, 2, artifact.Summary.ToolCalls)
	assert.Equal(t, 1, artifact.Summary.InvalidToolCalls)
	assert.Equal(t, 1, artifact.Summary.BlockedAttempts)
	assert.Equal(t, 18, artifact.Summary.TotalTokens)
	assert.Equal(t, 3, artifact.AgentRuns[0].ModelCalls[0].Usage.CachedPromptTokens)
	assert.True(t, artifact.AgentRuns[0].ToolCalls[0].IsNewEvidence)
	assert.NotEmpty(t, artifact.AgentRuns[0].NewEvidenceRefs)
	assert.Equal(t, "denied", artifact.AgentRuns[0].ToolCalls[1].Status)
}

func TestRecorderDoesNotCountRepeatedEvidenceAsNewAndAttributesFailure(t *testing.T) {
	recorder := New("incident-001", Versions{AgentVersion: "agent/v1", DatasetVersion: "dataset/v1"})
	for round := 0; round < 2; round++ {
		recorder.BeginAgentRun("investigate", workflow.Snapshot{State: workflow.StateInvestigating})
		recorder.RecordToolCall("", "query_metrics", workflow.AgentActionRead, `{}`, `{"data":{"sample_count":100}}`, time.Now(), nil, false)
		recorder.EndAgentRun(workflow.Snapshot{State: workflow.StateInvestigating}, nil)
	}
	artifact := recorder.Finish("", workflow.Snapshot{State: workflow.StateProbing}, errors.New("probe telemetry unavailable"))
	assert.NotEmpty(t, artifact.AgentRuns[0].NewEvidenceRefs)
	assert.Empty(t, artifact.AgentRuns[1].NewEvidenceRefs)
	require.NotNil(t, artifact.Failure)
	assert.Equal(t, "probing", artifact.Failure.Stage)
	assert.Equal(t, "failed", artifact.Outcome)
}
