package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/experience"
	"github.com/wen/opentalon/internal/runartifact"
	"github.com/wen/opentalon/internal/runmeta"
	"github.com/wen/opentalon/internal/skill"
)

type historicalReader struct{}

func (historicalReader) Search(context.Context, experience.Query) (experience.SearchResult, error) {
	return experience.SearchResult{Release: "pinned-v1", Trust: "historical_reference_not_current_evidence", Items: []experience.Summary{{ID: "old-exp", Version: 1, Title: "check old connections", Exceptions: []string{"do not use for upstream outage"}}}}, nil
}
func (historicalReader) Get(context.Context, string, int) (experience.Card, error) {
	return experience.Card{}, experience.ErrNotFound
}
func (historicalReader) Evidence(context.Context, string, int, string, string) (json.RawMessage, error) {
	return nil, experience.ErrNotFound
}
func TestExperienceIsVisibleWithSkillsButCannotSatisfyEvidenceGate(t *testing.T) {
	ctx := context.Background()
	service, id := testSimulator(t)
	flow := investigatingWorkflow(t, id)
	rec := runartifact.New(id, runmeta.Provenance{}, runmeta.Config{ExperienceRelease: "pinned-v1"})
	registry, e := skill.LoadDirectory("../../skills")
	require.NoError(t, e)
	session, e := skill.NewSession(registry, skill.DefaultMaxActive, rec.ValidateEvidenceRefs)
	require.NoError(t, e)
	m := &scriptedModel{response: func(n int) *schema.Message {
		if n == 1 {
			return schema.AssistantMessage("", []schema.ToolCall{{ID: "history-call", Function: schema.FunctionCall{Name: "search_experiences", Arguments: `{"query":"timeout"}`}}})
		}
		return schema.AssistantMessage("need current evidence", nil)
	}}
	a, e := NewToolOpsAgent(ctx, Config{Model: m, Platform: service, IncidentID: id, Workflow: flow, Skills: session, Artifact: rec, Experiences: historicalReader{}})
	require.NoError(t, e)
	rec.BeginAgentRun("investigate", flow.Snapshot())
	_, e = a.Run(ctx, "")
	rec.EndAgentRun(flow.Snapshot(), e)
	require.NoError(t, e)
	names, _ := m.snapshot()
	require.Contains(t, names, "search_experiences")
	snapshot := rec.Snapshot()
	require.Len(t, snapshot.AgentRuns[0].ToolCalls, 1)
	call := snapshot.AgentRuns[0].ToolCalls[0]
	require.False(t, call.IsNewEvidence)
	require.Empty(t, call.EvidenceRef)
	require.Error(t, rec.ValidateEvidenceRefs([]string{"history-call"}))
	restored, e := runartifact.Resume(snapshot, 0)
	require.NoError(t, e)
	require.Contains(t, experienceRecall(restored.Snapshot()), "pinned-v1")
	require.Contains(t, experienceRecall(restored.Snapshot()), "do not use for upstream outage")
}
