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

func TestRecorderSummarizesModelsEvidenceAndBlockedCalls(t *testing.T) {
	recorder := New("incident-001", Provenance{CodeVersion: "test-code", DatasetVersion: "test-data"}, RunConfig{Model: "test-model", AgentMaxSteps: 24})
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
	recorder.RecordFinalState(nil, FinalState{WorkflowState: workflow.StatePlanned})

	artifact := recorder.Finish("resolved", workflow.Snapshot{}, nil)
	assert.Equal(t, SchemaVersion, artifact.SchemaVersion)
	assert.Equal(t, "test-code", artifact.Provenance.CodeVersion)
	assert.Equal(t, "test-data", artifact.Provenance.DatasetVersion)
	assert.Equal(t, "test-model", artifact.RunConfig.Model)
	assert.Equal(t, workflow.StatePlanned, artifact.FinalState.WorkflowState)
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
	recorder := New("incident-001", Provenance{}, RunConfig{})
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

func TestRecorderNormalizesEmptyCollectionsToJSONArrays(t *testing.T) {
	recorder := New("incident-001", Provenance{CodeVersion: "code", DatasetVersion: "data"}, RunConfig{})
	artifact := recorder.Finish("escalated", workflow.Snapshot{State: workflow.StateEscalated}, nil)
	payload, err := json.Marshal(artifact)
	require.NoError(t, err)
	assert.NotContains(t, string(payload), `"plans":null`)
	assert.NotContains(t, string(payload), `"agent_runs":null`)
	assert.NotContains(t, string(payload), `"operations":null`)
	assert.NotContains(t, string(payload), `"workflow_history":null`)
}
