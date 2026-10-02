package app

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/controller"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/runartifact"
	"github.com/wen/opentalon/internal/scenario"
	"github.com/wen/opentalon/internal/simulator"
	"github.com/wen/opentalon/internal/storage"
	"github.com/wen/opentalon/internal/workflow"
)

type intentInvestigator struct {
	flow       *workflow.IncidentWorkflow
	incidentID string
}

func (p *intentInvestigator) IncidentID() string { return p.incidentID }

func (p *intentInvestigator) Investigate(context.Context, string) error {
	_, err := p.flow.SubmitExecutionIntent(workflow.ExecutionIntentDraft{
		Summary: "rollback mapping regression", RootCause: "mapping-v2 changed size to a string",
		EvidenceRefs: []string{"metric:error_rate", "log:invalid_parameter_type", "change:mapping-v2"},
		Stages: []workflow.ExecutionStageDraft{
			{StageID: "rollback", Goal: "rollback mapping", Actions: []workflow.IntendedAction{{
				Key: "rollback-mapping", ToolName: "rollback_mapping",
				Arguments: map[string]any{
					"tool_id": "generate_image", "target_version": "mapping-v1", "expected_version": "mapping-v2",
				},
			}}, CheckpointPolicy: workflow.CheckpointPolicy{DefaultDecision: workflow.CheckpointContinue}},
			{StageID: "probe", Goal: "verify the repaired route", Actions: []workflow.IntendedAction{{
				Key: "probe-route", Kind: workflow.ActionKindProbe, ToolName: "request_probe",
				Arguments: map[string]any{"route_id": "route-a", "policy_id": "default-safe-recovery", "idempotency_key": "probe-mapping"},
			}}, CheckpointPolicy: workflow.CheckpointPolicy{Rules: []workflow.CheckpointRule{{
				SourceActionID: "probe-route", OutputPath: "output.outcome", Equals: "healthy",
				Decision: workflow.CheckpointContinue, NextStageID: "recovery",
			}}, DefaultDecision: workflow.CheckpointNeedsAgent}},
			{StageID: "recovery", Goal: "restore baseline traffic", Actions: []workflow.IntendedAction{{
				Key: "recover-route", Kind: workflow.ActionKindRecovery, ToolName: "request_recovery",
				Arguments: map[string]any{"route_id": "route-a", "policy_id": "default-safe-recovery", "idempotency_key": "recover-mapping"},
			}}, CheckpointPolicy: workflow.CheckpointPolicy{Rules: []workflow.CheckpointRule{{
				SourceActionID: "recover-route", OutputPath: "output.outcome", Equals: "healthy",
				Decision: workflow.CheckpointSucceeded,
			}}, DefaultDecision: workflow.CheckpointNeedsAgent}},
		},
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
			return &intentInvestigator{flow: flow, incidentID: flow.Snapshot().IncidentID}, nil
		},
	})
	require.NoError(t, err)
	assert.Equal(t, controller.StopResolved, result.Controller.Reason)
	assert.Equal(t, workflow.StateResolved, result.Controller.Snapshot.State)
	assert.Equal(t, 80, result.World.Routes["route-a"].Weight)
	assert.Equal(t, 20, result.World.Routes["route-b"].Weight)
	assert.Equal(t, "talon.run-artifact/v3", result.Artifact.SchemaVersion)
	assert.Equal(t, "toolops-v1", result.Artifact.Provenance.DatasetVersion)
	assert.NotEmpty(t, result.Artifact.Provenance.CodeVersion)
	assert.Equal(t, "toolops-agent/v4", result.Artifact.Provenance.PromptVersion)
	assert.Len(t, result.Artifact.Provenance.PromptDigest, 64)
	assert.Equal(t, 24, result.Artifact.RunConfig.AgentMaxSteps)
	assert.True(t, result.Artifact.RunConfig.AutoApprove)
	assert.Equal(t, "completed", result.Artifact.Outcome)
	assert.Equal(t, string(controller.StopResolved), result.Artifact.StopReason)
	assert.Equal(t, 1, result.Artifact.Summary.AgentRuns)
	require.Len(t, result.Artifact.ExecutionIntents, 1)
	require.Len(t, result.Artifact.AgentRuns[0].ExecutionIntents, 1)
	assert.Equal(t, "rollback mapping regression", result.Artifact.ExecutionIntents[0].Summary)
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
	assert.Contains(t, output.String(), "executing -> checkpoint")
	assert.Contains(t, output.String(), "checkpoint -> resolved")
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
			return &intentInvestigator{flow: flow, incidentID: flow.Snapshot().IncidentID}, nil
		},
	})
	require.NoError(t, err)
	assert.Equal(t, controller.StopAwaitingApproval, result.Controller.Reason)
	assert.Equal(t, workflow.StateAwaitingApproval, result.Controller.Snapshot.State)
	assert.Equal(t, 10, result.World.Routes["route-a"].Weight)
	assert.Equal(t, "completed", result.Artifact.Outcome)
	assert.Equal(t, string(controller.StopAwaitingApproval), result.Artifact.StopReason)
}

func TestRunAutoApprovalDoesNotDecidePreviousRunRequests(t *testing.T) {
	ctx := context.Background()
	database, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "talon.db"))
	require.NoError(t, err)
	defer database.Close()
	cfg := Config{
		DatasetRoot: testDatasetRoot(t), Storage: database,
		ClockPollInterval: time.Millisecond, WorkerRetryInterval: time.Millisecond,
		InvestigatorFactory: func(flow *workflow.IncidentWorkflow, _ platform.ToolOpsPlatform) (controller.Investigator, error) {
			return &intentInvestigator{flow: flow, incidentID: flow.Snapshot().IncidentID}, nil
		},
	}
	first, err := Run(ctx, cfg)
	require.NoError(t, err)
	require.Equal(t, controller.StopAwaitingApproval, first.Controller.Reason)
	oldPending, err := database.Approvals().ListPending(ctx)
	require.NoError(t, err)
	require.Len(t, oldPending, 1)

	cfg.AutoApprove = true
	second, err := Run(ctx, cfg)
	require.NoError(t, err)
	assert.Equal(t, controller.StopResolved, second.Controller.Reason)
	assert.NotEqual(t, first.Artifact.RunID, second.Artifact.RunID)
	assert.Equal(t, first.Controller.Snapshot.IncidentID, second.Controller.Snapshot.IncidentID)
	remaining, err := database.Approvals().ListPending(ctx)
	require.NoError(t, err)
	assert.Equal(t, oldPending, remaining, "the previous run's approval must remain untouched")
	oldCheckpoint, err := database.Checkpoints().Get(ctx, first.Artifact.RunID)
	require.NoError(t, err)
	assert.Equal(t, workflow.StateAwaitingApproval, oldCheckpoint.Workflow.State)
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

func TestAgentStartOffsetUsesExplicitIncidentTime(t *testing.T) {
	document := scenario.Scenario{
		Metadata: scenario.Metadata{ID: "detected-after-window"},
		Clock:    scenario.Clock{IncidentAt: "9m"},
		Timeline: []scenario.TimelineEvent{{At: "4m"}, {At: "8m"}},
	}
	offset, err := agentStartOffset(document)
	require.NoError(t, err)
	assert.Equal(t, 9*time.Minute, offset)
}

func TestAgentStartOffsetKeepsLegacyFirstEventDefault(t *testing.T) {
	document := scenario.Scenario{
		Metadata: scenario.Metadata{ID: "legacy-start"},
		Timeline: []scenario.TimelineEvent{{At: "8m"}, {At: "4m"}},
	}
	offset, err := agentStartOffset(document)
	require.NoError(t, err)
	assert.Equal(t, 4*time.Minute, offset)
}

func testDatasetRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", "data", "toolops-v1"))
}

var _ controller.Investigator = (*intentInvestigator)(nil)

func activeArtifactConfig(values []runartifact.ConfigState) string {
	for _, value := range values {
		if value.Active {
			return value.ID
		}
	}
	return ""
}

func TestRunPropagatesAuditWriteFailures(t *testing.T) {
	for _, when := range []string{"investigation", "final"} {
		t.Run(when, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audit.db")
			db, err := storage.OpenSQLite(context.Background(), path)
			require.NoError(t, err)
			defer db.Close()
			faults, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			defer faults.Close()
			condition := "json_array_length(NEW.artifact, '$.agent_runs') > 0"
			if when == "final" {
				condition = "NEW.outcome != 'running'"
			}
			_, err = faults.Exec(fmt.Sprintf(`CREATE TRIGGER fail_audit BEFORE UPDATE ON run_artifacts WHEN %s BEGIN SELECT RAISE(ABORT, 'injected audit failure'); END`, condition))
			require.NoError(t, err)
			result, err := Run(context.Background(), Config{
				DatasetRoot: testDatasetRoot(t), Storage: db, AutoApprove: true,
				ClockPollInterval: time.Millisecond, WorkerRetryInterval: time.Millisecond,
				InvestigatorFactory: func(flow *workflow.IncidentWorkflow, _ platform.ToolOpsPlatform) (controller.Investigator, error) {
					return &intentInvestigator{flow: flow, incidentID: flow.Snapshot().IncidentID}, nil
				},
			})
			require.ErrorContains(t, err, "injected audit failure")
			assert.Equal(t, "failed", result.Artifact.Outcome)
			if when == "investigation" {
				assert.Empty(t, result.World.Operations)
			} else {
				assert.Equal(t, workflow.StateResolved, result.Controller.Snapshot.State)
				assert.Contains(t, err.Error(), "persist final run artifact")
			}
		})
	}
}

// Cancellation while the virtual clock is active must finish audit and release
// the clock before Run returns; the captured world remains stable afterwards.
func TestRunCancellationStopsClockBeforeFinalAudit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "cancel.db"))
	require.NoError(t, err)
	defer db.Close()
	writer := &cancelOnOperation{cancel: cancel}
	var service *simulator.Simulator
	done := make(chan struct{})
	var result Result
	var runErr error
	go func() {
		defer close(done)
		result, runErr = Run(ctx, Config{
			DatasetRoot: testDatasetRoot(t), Storage: db, Output: writer, AutoApprove: true,
			ClockPollInterval: time.Millisecond, WorkerRetryInterval: time.Millisecond,
			InvestigatorFactory: func(flow *workflow.IncidentWorkflow, platformService platform.ToolOpsPlatform) (controller.Investigator, error) {
				service = platformService.(*simulator.Simulator)
				return &intentInvestigator{flow: flow, incidentID: flow.Snapshot().IncidentID}, nil
			},
		})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("canceled run did not finish within persistence timeout")
	}
	require.ErrorIs(t, runErr, context.Canceled)
	require.True(t, writer.canceled)
	persisted, err := db.RunArtifacts().Get(context.Background(), result.Artifact.RunID)
	require.NoError(t, err)
	assert.Equal(t, result.Artifact, persisted)
	assert.Equal(t, result.World.Now, result.Artifact.FinalState.VirtualTime)
	assert.Equal(t, result.World, service.Snapshot())
	// A former clock iteration must not outlive the returned result.
	time.Sleep(5 * time.Millisecond)
	assert.Equal(t, result.World, service.Snapshot())
}

type cancelOnOperation struct {
	cancel   context.CancelFunc
	canceled bool
}

func (w *cancelOnOperation) Write(value []byte) (int, error) {
	if !w.canceled && bytes.Contains(value, []byte("[operation]")) {
		w.canceled = true
		w.cancel()
	}
	return len(value), nil
}
