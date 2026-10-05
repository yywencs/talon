package runartifact

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/runmeta"
	"github.com/wen/opentalon/internal/workflow"
	"testing"
	"time"
)

func TestResumePreservesUsageFloorAcrossRepeatedInterruptions(t *testing.T) {
	original := New("incident", runmeta.Provenance{}, runmeta.Config{}).Snapshot()
	first, err := Resume(original, 3)
	require.NoError(t, err)
	first.BeginAgentRun("fresh observations", workflow.Snapshot{})
	first.RecordModelCall(time.Now(), nil, nil)
	savedAudit := first.Snapshot()
	assert.Equal(t, 4, savedAudit.Summary.ModelCalls)
	// Audit committed the latest call, but the checkpoint still has the previous
	// floor. Recovering must retain both the missing old calls and this new call.
	second, err := Resume(savedAudit, 3)
	require.NoError(t, err)
	assert.Equal(t, 4, second.ModelCallsUsed())
	assert.Equal(t, original.RunID, second.Metadata().RunID)
	second.BeginAgentRun("another investigation", workflow.Snapshot{})
	second.RecordModelCall(time.Now(), nil, nil)
	assert.Equal(t, 5, second.ModelCallsUsed())
	assert.Equal(t, 5, second.Snapshot().Summary.ModelCalls)
	assert.Len(t, savedAudit.AgentRuns, 1, "resumed recorder must not mutate saved audit")
}
