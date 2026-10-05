package workflow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProcessResumeConsumesBudgetAndSurvivesRestore(t *testing.T) {
	flow, err := NewIncidentWorkflow(Config{IncidentID: "incident", Limits: ExecutionLimits{MaxAgentResumes: 1}})
	require.NoError(t, err)
	require.NoError(t, flow.ResumeInvestigation()) // No previous investigation to resume.
	require.NoError(t, flow.ResumeInvestigation())
	saved := flow.Snapshot()
	assert.Equal(t, 1, saved.AgentResumesUsed)
	restored, err := Restore(saved)
	require.NoError(t, err)
	require.NoError(t, restored.ResumeInvestigation())
	assert.Equal(t, StateEscalated, restored.Snapshot().State)
	assert.Equal(t, 1, restored.Snapshot().AgentResumesUsed)
	_, err = Restore(restored.Snapshot())
	require.NoError(t, err)
	require.ErrorIs(t, restored.ResumeInvestigation(), ErrInvalidTransition)
}
