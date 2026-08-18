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
	recorder := New("incident-001", Provenance{CodeVersion: "test-code", DatasetVersion: "test-data"}, RunConfig{ModelProvider: "test-provider", Model: "test-model", AgentMaxSteps: 24})
	recorder.BeginAgentRun("investigate", workflow.Snapshot{State: workflow.StateInvestigating})
	started := time.Now().Add(-time.Millisecond)
	recorder.RecordModelCallWithContext(started, &schema.Message{ResponseMeta: &schema.ResponseMeta{
		FinishReason: "tool_calls",
		Usage: &schema.TokenUsage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18,
			PromptTokenDetails:      schema.PromptTokenDetails{CachedTokens: 3},
			CompletionTokensDetails: schema.CompletionTokensDetails{ReasoningTokens: 2}},
	}}, nil, IncidentContextSnapshot{IncidentID: "incident-001", Objective: "investigate"})
	recorder.RecordToolCall("call-1", "query_logs", workflow.AgentActionRead, `{}`, `{"data":[{"code":"connection_refused"}],"evidence_ids":["log.connection_refused"]}`, started, nil, false)
	recorder.RecordToolCall("call-2", "submit_plan", workflow.AgentActionSubmitPlan, `{}`, `{"data":null,"error":"not allowed in state planned"}`, started, nil, true)
	require.NoError(t, recorder.ValidateEvidenceRefs([]string{"call-1"}))
	current := recorder.Snapshot()
	require.NoError(t, recorder.ValidateEvidenceRefs([]string{current.AgentRuns[0].ToolCalls[0].EvidenceRef}))
	require.ErrorContains(t, recorder.ValidateEvidenceRefs([]string{"call-2"}), "does not identify a successful read")
	recorder.EndAgentRun(workflow.Snapshot{State: workflow.StatePlanned}, nil)
	recorder.RecordFinalState(nil, FinalState{WorkflowState: workflow.StatePlanned})

	artifact := recorder.Finish("resolved", workflow.Snapshot{}, nil)
	assert.Equal(t, SchemaVersion, artifact.SchemaVersion)
	assert.ElementsMatch(t, currentCapabilities, artifact.Capabilities)
	assert.Equal(t, "test-code", artifact.Provenance.CodeVersion)
	assert.Equal(t, "test-data", artifact.Provenance.DatasetVersion)
	assert.Equal(t, "test-model", artifact.RunConfig.Model)
	assert.Equal(t, IncidentContextSchemaVersion, artifact.RunConfig.ContextVersion)
	assert.Equal(t, workflow.StatePlanned, artifact.FinalState.WorkflowState)
	require.Len(t, artifact.AgentRuns, 1)
	assert.Equal(t, 1, artifact.Summary.ModelCalls)
	assert.Equal(t, 2, artifact.Summary.ToolCalls)
	assert.Equal(t, 1, artifact.Summary.InvalidToolCalls)
	assert.Equal(t, 1, artifact.Summary.BlockedAttempts)
	assert.Equal(t, 18, artifact.Summary.TotalTokens)
	assert.Equal(t, 3, artifact.AgentRuns[0].ModelCalls[0].Usage.CachedPromptTokens)
	assert.Equal(t, "test-provider", artifact.AgentRuns[0].ModelCalls[0].ModelProvider)
	assert.Equal(t, "test-model", artifact.AgentRuns[0].ModelCalls[0].Model)
	require.NotNil(t, artifact.AgentRuns[0].ModelCalls[0].ContextSnapshot)
	assert.Equal(t, "incident-001", artifact.AgentRuns[0].ModelCalls[0].ContextSnapshot.IncidentID)
	assert.True(t, artifact.AgentRuns[0].ToolCalls[0].IsNewEvidence)
	assert.Equal(t, []string{"log.connection_refused"}, artifact.AgentRuns[0].ToolCalls[0].EvidenceIDs)
	assert.NotEmpty(t, artifact.AgentRuns[0].NewEvidenceRefs)
	assert.Contains(t, artifact.Experience.Fields, "symptoms")
	assert.Contains(t, artifact.Experience.Fields, "applicability")
	assert.Equal(t, "denied", artifact.AgentRuns[0].ToolCalls[1].Status)
}

func TestRecorderStoresSealedIncidentContextOnCurrentAgentRun(t *testing.T) {
	recorder := New("incident-001", Provenance{}, RunConfig{})
	recorder.BeginAgentRun("investigate", workflow.Snapshot{State: workflow.StateInvestigating})
	require.NoError(t, recorder.RecordContextSnapshot(IncidentContextSnapshot{
		IncidentID: "incident-001", Objective: "investigate",
		Workflow: IncidentContextWorkflow{State: string(workflow.StateInvestigating)},
		Evidence: []IncidentContextEvidence{{EvidenceRef: "tool:query_logs:abc", EvidenceIDs: []string{"log.connection_refused"}}},
	}))

	artifact := recorder.Snapshot()
	require.Len(t, artifact.AgentRuns, 1)
	require.NotNil(t, artifact.AgentRuns[0].ContextSnapshot)
	contextSnapshot := artifact.AgentRuns[0].ContextSnapshot
	assert.Equal(t, IncidentContextSchemaVersion, contextSnapshot.SchemaVersion)
	assert.Equal(t, "incident-001", contextSnapshot.IncidentID)
	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, contextSnapshot.Digest)
	assert.NotNil(t, contextSnapshot.ActiveSkills)
	assert.NotNil(t, contextSnapshot.Plans)
	assert.NotNil(t, contextSnapshot.Constraints)
}

func TestRecorderGetsEvidenceOnlyByStableReference(t *testing.T) {
	recorder := New("incident-001", Provenance{}, RunConfig{})
	recorder.BeginAgentRun("investigate", workflow.Snapshot{State: workflow.StateInvestigating})
	recorder.RecordToolCall("call-logs", "query_logs", workflow.AgentActionRead, `{}`,
		`{"data":[{"code":"connection_refused"}],"evidence_ids":["log.connection_refused"]}`,
		time.Now(), nil, false)
	call := recorder.Snapshot().AgentRuns[0].ToolCalls[0]

	record, err := recorder.GetEvidence(call.EvidenceRef)
	require.NoError(t, err)
	assert.Equal(t, call.EvidenceRef, record.EvidenceRef)
	assert.Equal(t, []string{"log.connection_refused"}, record.EvidenceIDs)
	assert.Equal(t, "query_logs", record.SourceTool)
	assert.JSONEq(t, string(call.Output), string(record.Output))
	_, err = recorder.GetEvidence("call-logs")
	require.ErrorContains(t, err, "was not found in the current Incident")
}

func TestRecorderDoesNotCountRepeatedEvidenceAsNewAndAttributesFailure(t *testing.T) {
	recorder := New("incident-001", Provenance{}, RunConfig{})
	for round := 0; round < 2; round++ {
		recorder.BeginAgentRun("investigate", workflow.Snapshot{State: workflow.StateInvestigating})
		recorder.RecordToolCall("", "query_metrics", workflow.AgentActionRead, `{}`, `{"data":{"sample_count":100}}`, time.Now(), nil, false)
		recorder.EndAgentRun(workflow.Snapshot{State: workflow.StateInvestigating}, nil)
	}
	artifact := recorder.Finish("", workflow.Snapshot{State: workflow.StateCheckpoint}, errors.New("checkpoint telemetry unavailable"))
	assert.NotEmpty(t, artifact.AgentRuns[0].NewEvidenceRefs)
	assert.Empty(t, artifact.AgentRuns[1].NewEvidenceRefs)
	require.NotNil(t, artifact.Failure)
	assert.Equal(t, "checkpoint", artifact.Failure.Stage)
	assert.Equal(t, "failed", artifact.Outcome)
	assert.NotContains(t, artifact.Experience.Fields, "evidence_after_failed_probe")
}

func TestRecorderPersistsNormalizedStageFailure(t *testing.T) {
	recorder := New("incident-001", Provenance{}, RunConfig{})
	snapshot := workflow.Snapshot{
		State: workflow.StateCheckpoint,
		Failures: []workflow.StageFailure{{
			Stage: workflow.FailureStageCheckpoint, Category: workflow.FailureCategoryInvalidResponse,
			Code: "invalid_checkpoint_outcome", SafeSummary: "Checkpoint 返回了无法识别的成功结果",
			Message: "untrusted raw platform message", NextAction: workflow.FailureNextEscalate,
			Fallback: false, OperationID: "operation-1",
		}},
	}
	artifact := recorder.Finish("", snapshot, errors.New("run traffic probe: invalid outcome"))

	require.Len(t, artifact.StageFailures, 1)
	require.NotNil(t, artifact.Failure)
	assert.Equal(t, "checkpoint", artifact.Failure.Stage)
	assert.Equal(t, "invalid_response", artifact.Failure.Category)
	assert.Equal(t, "invalid_checkpoint_outcome", artifact.Failure.Code)
	assert.Equal(t, "Checkpoint 返回了无法识别的成功结果", artifact.Failure.SafeSummary)
	assert.Equal(t, "escalate", artifact.Failure.NextAction)
}

func TestRecorderPersistsDynamicStageResolutionAndCheckpointTrail(t *testing.T) {
	recorder := New("dynamic", Provenance{CodeVersion: "test"}, RunConfig{})
	snapshot := workflow.Snapshot{
		State: workflow.StateResolved,
		ResolvedActions: []workflow.ResolvedAction{{PlanID: "plan", StageID: "probe", ActionID: "probe-action",
			TemplateDigest: "template", Digest: "resolved", ToolName: "probe_route",
			OriginalArguments: map[string]any{}, Arguments: map[string]any{"route_id": "route-new"},
			Sources: []workflow.ResolvedArgumentSource{{Argument: "route_id", SourceResultID: "refresh:result",
				Reference: workflow.ActionOutputReference{SourceActionID: "refresh", OutputPath: "output.route.id", ExpectedType: workflow.ActionOutputString, Required: true}}}}},
		ActionResults: []workflow.ActionResult{{ResultID: "refresh:result", PlanID: "plan", StageID: "refresh",
			ActionID: "refresh", ActionDigest: "digest", OperationID: "operation", OperationStatus: "succeeded",
			Output: map[string]any{"route": map[string]any{"id": "route-new"}}, EvidenceRef: "action:refresh:evidence"}},
		AllPlanDryRuns:  []workflow.PlanDryRun{{PlanID: "plan", ActionID: "probe-action", ActionDigest: "resolved", Status: workflow.PlanDryRunSucceeded}},
		AllPlanPolicies: []workflow.PlanPolicyDecision{{PlanID: "plan", ActionID: "probe-action", ActionDigest: "resolved", Outcome: workflow.PlanPolicyAutoApproved}},
		Checkpoints: []workflow.DecisionCheckpoint{{CheckpointID: "checkpoint", StageID: "probe", Decision: workflow.CheckpointSucceeded,
			DecisionReason: "healthy", NewEvidenceRefs: []string{"action:refresh:evidence"}}},
	}
	recorder.RecordWorkflowCheckpoint(snapshot)
	running := recorder.Snapshot()
	require.Len(t, running.ActionResults, 1)
	require.Len(t, running.Checkpoints, 1)
	artifact := recorder.Finish("resolved", snapshot, nil)
	require.Len(t, artifact.ResolvedActions, 1)
	assert.Equal(t, "route-new", artifact.ResolvedActions[0].Arguments["route_id"])
	require.Len(t, artifact.ActionResults, 1)
	require.Len(t, artifact.PlanDryRuns, 1)
	require.Len(t, artifact.PlanPolicies, 1)
	require.Len(t, artifact.Checkpoints, 1)
	assert.Equal(t, workflow.CheckpointSucceeded, artifact.Checkpoints[0].Decision)
	assert.Contains(t, artifact.Capabilities, CapabilityDynamicExecutionStages)
	assert.Contains(t, artifact.Capabilities, CapabilityTypedActionOutputReferences)
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
	assert.NotContains(t, string(payload), `"experience":{"fields":null`)
}
