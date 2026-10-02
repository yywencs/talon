package workflow

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRestorePreservesStageProgressAndOwnsSnapshot(t *testing.T) {
	flow, submission := submitDynamicRouteIntent(t, ActionOutputString)
	resolveAndCompleteFirstDynamicStage(t, flow, submission, map[string]any{"route": map[string]any{"id": "route-restored"}})
	_, err := flow.EvaluateCheckpoint()
	require.NoError(t, err)
	snapshot := flow.Snapshot()
	data, err := json.Marshal(snapshot)
	require.NoError(t, err)
	var fromDB Snapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&fromDB))
	restored, err := Restore(fromDB)
	require.NoError(t, err)
	roundTrip, err := json.Marshal(restored.Snapshot())
	require.NoError(t, err)
	assert.Equal(t, string(data), string(roundTrip), "omitempty slices may decode as nil but serialized state must be unchanged")
	assert.Equal(t, 1, restored.Snapshot().ActiveStageIndex)
	assert.Equal(t, 1, restored.Snapshot().StagesExecuted)

	fromDB.ActionResults[0].Output["route"].(map[string]any)["id"] = "changed"
	fromDB.ExecutionIntent.Stages[0].Actions[0].ToolName = "changed"
	fromDB.History[0].Metadata = map[string]string{"changed": "yes"}
	resolved, err := restored.ResolveCurrentStage()
	require.NoError(t, err)
	require.Len(t, resolved, 1)
	assert.Equal(t, "route-restored", resolved[0].Arguments["route_id"])
	assert.Equal(t, snapshot.Version, restored.Snapshot().Version, "restoration must not append transitions")
	assert.Equal(t, snapshot.ActionsAccepted, restored.Snapshot().ActionsAccepted)
	assert.Equal(t, snapshot.IntentIDPrefix, restored.Snapshot().IntentIDPrefix)
	assert.Equal(t, snapshot.ExecutionIntent.ID, restored.Snapshot().ExecutionIntent.ID)
	_, err = restored.Apply(Event{Type: EventStartInvestigation, Actor: ActorController})
	require.ErrorIs(t, err, ErrInvalidTransition)
}

func TestRestoreRejectsInconsistentSnapshot(t *testing.T) {
	flow, submission := submitDynamicRouteIntent(t, ActionOutputString)
	resolveAndCompleteFirstDynamicStage(t, flow, submission, map[string]any{"route": map[string]any{"id": "route-new"}})
	_, err := flow.EvaluateCheckpoint()
	require.NoError(t, err)
	for name, change := range map[string]func(*Snapshot){
		"unknown state":    func(s *Snapshot) { s.State = "unknown" },
		"missing prefix":   func(s *Snapshot) { s.IntentIDPrefix = "" },
		"history version":  func(s *Snapshot) { s.Version++ },
		"history state":    func(s *Snapshot) { s.History[0].To = StateResolved },
		"history actor":    func(s *Snapshot) { s.History[0].Actor = ActorAgent },
		"stage index":      func(s *Snapshot) { s.ActiveStageIndex = 999 },
		"rewound stage":    func(s *Snapshot) { s.ActiveStageIndex = 0 },
		"missing intent":   func(s *Snapshot) { s.ExecutionIntent = nil },
		"changed intent":   func(s *Snapshot) { s.ExecutionIntent.Stages[0].Actions[0].ToolName = "changed" },
		"reset budget":     func(s *Snapshot) { s.ActionsAccepted = 0 },
		"reset stages":     func(s *Snapshot) { s.StagesExecuted = 0 },
		"invalid limits":   func(s *Snapshot) { s.Limits.MaxActions = 0 },
		"foreign result":   func(s *Snapshot) { s.ActionResults[0].IntentID = "other" },
		"foreign resolved": func(s *Snapshot) { s.ResolvedActions[0].TemplateDigest = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			snapshot := flow.Snapshot()
			change(&snapshot)
			restored, err := Restore(snapshot)
			require.Error(t, err)
			assert.Nil(t, restored)
		})
	}
}

func TestRestoreRetainsIdentityAndBudgetsAcrossNewIntent(t *testing.T) {
	flow, _ := submitDynamicRouteIntent(t, ActionOutputString)
	_, err := flow.Apply(Event{Type: EventEscalated, Actor: ActorHuman, Reason: "review"})
	require.NoError(t, err)
	restored, err := Restore(flow.Snapshot())
	require.NoError(t, err)
	assert.Equal(t, flow.Snapshot(), restored.Snapshot())
	_, err = restored.Apply(Event{Type: EventHumanResumed, Actor: ActorHuman})
	require.NoError(t, err)
	before := restored.Snapshot()
	submitted, err := restored.SubmitExecutionIntent(ExecutionIntentDraft{
		Summary: "new investigation", RootCause: "new evidence", EvidenceRefs: []string{"new-evidence"},
		Stages: []ExecutionStageDraft{{StageID: "new", Goal: "verify", Actions: []IntendedAction{{ToolName: "verify"}}}},
	})
	require.NoError(t, err)
	assert.NotEqual(t, before.ExecutionIntent.ID, submitted.ExecutionIntent.ID)
	assert.Equal(t, before.ActionsAccepted+1, restored.Snapshot().ActionsAccepted)
	assert.Equal(t, before.Version+1, restored.Snapshot().Version)
	assert.False(t, submitted.Transition.At.IsZero())
	_, err = Restore(restored.Snapshot())
	require.NoError(t, err)
}

func TestRestoredNumericRuleMatchesNewActionOutput(t *testing.T) {
	flow, err := NewIncidentWorkflow(Config{IncidentID: "numeric-rule"})
	require.NoError(t, err)
	_, err = flow.Apply(Event{Type: EventStartInvestigation, Actor: ActorController})
	require.NoError(t, err)
	_, err = flow.SubmitExecutionIntent(ExecutionIntentDraft{
		Summary: "verify", RootCause: "test", EvidenceRefs: []string{"evidence"},
		Stages: []ExecutionStageDraft{{StageID: "verify", Goal: "verify", Actions: []IntendedAction{{Key: "check", ToolName: "check"}},
			CheckpointPolicy: CheckpointPolicy{Rules: []CheckpointRule{{SourceActionID: "check", OutputPath: "output.count",
				Equals: 7, Decision: CheckpointSucceeded}}, DefaultDecision: CheckpointNeedsAgent}}},
	})
	require.NoError(t, err)
	encoded, err := json.Marshal(flow.Snapshot())
	require.NoError(t, err)
	var snapshot Snapshot
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&snapshot))
	restored, err := Restore(snapshot)
	require.NoError(t, err)
	actions, err := restored.ResolveCurrentStage()
	require.NoError(t, err)
	_, err = restored.Apply(Event{Type: EventExecutionAuthorized, Actor: ActorWorkflow})
	require.NoError(t, err)
	_, err = restored.RecordActionResult(ActionResult{IntentID: snapshot.ExecutionIntent.ID, StageID: "verify",
		ActionID: actions[0].ActionID, ActionDigest: actions[0].Digest, OperationID: "test-operation",
		OperationStatus: "succeeded", Output: map[string]any{"count": 7}})
	require.NoError(t, err)
	_, err = restored.CompleteCurrentStage("verified")
	require.NoError(t, err)
	decision, err := restored.EvaluateCheckpoint()
	require.NoError(t, err)
	assert.Equal(t, CheckpointSucceeded, decision.Decision)
}
