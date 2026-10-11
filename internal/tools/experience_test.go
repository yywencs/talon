package tools

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/experience"
	"github.com/wen/opentalon/internal/workflow"
	"testing"
)

type experienceReaderStub struct{}

func (experienceReaderStub) Search(context.Context, experience.Query) (experience.SearchResult, error) {
	return experience.SearchResult{Release: "fixed", Trust: "historical_reference_not_current_evidence"}, nil
}
func (experienceReaderStub) Get(context.Context, string, int) (experience.Card, error) {
	return experience.Card{}, experience.ErrNotFound
}
func (experienceReaderStub) Evidence(context.Context, string, int, string, string) (json.RawMessage, error) {
	return nil, experience.ErrNotFound
}
func TestExperienceToolsNeverProduceCurrentEvidence(t *testing.T) {
	items, e := buildExperienceTools(experienceReaderStub{})
	require.NoError(t, e)
	require.Len(t, items, 3)
	for _, item := range items {
		info, e := item.Info(context.Background())
		require.NoError(t, e)
		action, ok := agentActionForTool(info.Name)
		require.True(t, ok)
		require.Equal(t, workflow.AgentActionRecallEvidence, action)
	}
	v, e := items[0].InvokableRun(context.Background(), `{"query":"timeout"}`)
	require.NoError(t, e)
	require.Contains(t, v, "historical_reference_not_current_evidence")
	require.NotContains(t, v, "evidence_ref")
}
