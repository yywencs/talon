package app

import (
	"bytes"
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/controller"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/runartifact"
	"github.com/wen/opentalon/internal/storage"
	"github.com/wen/opentalon/internal/workflow"
)

type planInvestigator struct {
	flow       *workflow.IncidentWorkflow
	incidentID string
}

func (p *planInvestigator) IncidentID() string { return p.incidentID }

func (p *planInvestigator) Investigate(context.Context, string) error {
	_, err := p.flow.SubmitPlan(workflow.PlanDraft{
		Summary: "rollback mapping regression", RootCause: "mapping-v2 changed size to a string",
		EvidenceRefs: []string{"metric:error_rate", "log:invalid_parameter_type", "change:mapping-v2"},
		Actions: []workflow.PlannedAction{{
			ToolName: "rollback_mapping",
			Arguments: map[string]any{
				"tool_id": "generate_image", "target_version": "mapping-v1", "expected_version": "mapping-v2",
			},
		}},
		ProbeRouteID: "route-a", RecoveryPolicyID: "default-safe-recovery",
	})
	return err
}

func TestRunCompletesScriptedEndToEndScenario(t *testing.T) {
	database, err := storage.OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "talon.db"))
	require.NoError(t, err)
	defer database.Close()
	var output bytes.Buffer

	result, err := Run(context.Background(), Config{
		DatasetRoot: testDatasetRoot(t), ScenarioID: defaultScenarioID,
		Storage: database, Output: &output, AutoApprove: true,
		ClockPollInterval: time.Millisecond, WorkerRetryInterval: time.Millisecond,
		InvestigatorFactory: func(flow *workflow.IncidentWorkflow, _ platform.ToolOpsPlatform) (controller.Investigator, error) {
			return &planInvestigator{flow: flow, incidentID: flow.Snapshot().IncidentID}, nil
		},
	})
	require.NoError(t, err)
	assert.Equal(t, controller.StopResolved, result.Controller.Reason)
	assert.Equal(t, workflow.StateResolved, result.Controller.Snapshot.State)
	assert.Equal(t, 80, result.World.Routes["route-a"].Weight)
	assert.Equal(t, 20, result.World.Routes["route-b"].Weight)
	assert.Equal(t, "talon.run-artifact/v2", result.Artifact.SchemaVersion)
	assert.Equal(t, "toolops-v1", result.Artifact.Provenance.DatasetVersion)
	assert.NotEmpty(t, result.Artifact.Provenance.CodeVersion)
	assert.Equal(t, 24, result.Artifact.RunConfig.AgentMaxSteps)
	assert.True(t, result.Artifact.RunConfig.AutoApprove)
	assert.Equal(t, "completed", result.Artifact.Outcome)
	assert.Equal(t, string(controller.StopResolved), result.Artifact.StopReason)
	assert.Equal(t, 1, result.Artifact.Summary.AgentRuns)
	require.Len(t, result.Artifact.Plans, 1)
	require.Len(t, result.Artifact.AgentRuns[0].Plans, 1)
	assert.Equal(t, "rollback mapping regression", result.Artifact.Plans[0].Summary)
	assert.NotEmpty(t, result.Artifact.WorkflowHistory)
	assert.Equal(t, workflow.StateResolved, result.Artifact.FinalState.WorkflowState)
	require.Len(t, result.Artifact.FinalState.Routes, 2)
	assert.Equal(t, "mapping-v1", activeArtifactConfig(result.Artifact.FinalState.Configs))
	assert.NotEmpty(t, result.Artifact.Operations)
	persisted, err := database.RunArtifacts().Get(context.Background(), result.Artifact.RunID)
	require.NoError(t, err)
	assert.Equal(t, result.Artifact, persisted)
	assert.Contains(t, output.String(), "SIMULATOR AUTO-APPROVE")
	assert.Contains(t, output.String(), "code_version=")
	assert.Contains(t, output.String(), "dataset_version=toolops-v1")
	assert.Contains(t, output.String(), "probing -> recovering")
	assert.Contains(t, output.String(), "recovering -> resolved")
	assert.Contains(t, output.String(), "[result] reason=resolved state=resolved")
}

func TestRunStopsAtApprovalWhenAutoApprovalDisabled(t *testing.T) {
	database, err := storage.OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "talon.db"))
	require.NoError(t, err)
	defer database.Close()

	result, err := Run(context.Background(), Config{
		DatasetRoot: testDatasetRoot(t), ScenarioID: defaultScenarioID,
		Storage: database, AutoApprove: false,
		ClockPollInterval: time.Millisecond, WorkerRetryInterval: time.Millisecond,
		InvestigatorFactory: func(flow *workflow.IncidentWorkflow, _ platform.ToolOpsPlatform) (controller.Investigator, error) {
			return &planInvestigator{flow: flow, incidentID: flow.Snapshot().IncidentID}, nil
		},
	})
	require.NoError(t, err)
	assert.Equal(t, controller.StopAwaitingApproval, result.Controller.Reason)
	assert.Equal(t, workflow.StateAwaitingApproval, result.Controller.Snapshot.State)
	assert.Equal(t, 10, result.World.Routes["route-a"].Weight)
	assert.Equal(t, "completed", result.Artifact.Outcome)
	assert.Equal(t, string(controller.StopAwaitingApproval), result.Artifact.StopReason)
}

func TestRunPersistsFailedArtifact(t *testing.T) {
	database, err := storage.OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "talon.db"))
	require.NoError(t, err)
	defer database.Close()

	result, err := Run(context.Background(), Config{
		DatasetRoot: testDatasetRoot(t), ScenarioID: defaultScenarioID, Storage: database,
	})
	require.ErrorContains(t, err, "model is required")
	assert.Equal(t, "failed", result.Artifact.Outcome)
	require.NotNil(t, result.Artifact.Failure)
	persisted, getErr := database.RunArtifacts().Get(context.Background(), result.Artifact.RunID)
	require.NoError(t, getErr)
	assert.Equal(t, result.Artifact, persisted)
}

func testDatasetRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", "data", "toolops-v1"))
}

var _ controller.Investigator = (*planInvestigator)(nil)

func activeArtifactConfig(values []runartifact.ConfigState) string {
	for _, value := range values {
		if value.Active {
			return value.ID
		}
	}
	return ""
}
