package app

import (
	"context"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/controller"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/runmeta"
	"github.com/wen/opentalon/internal/scenario"
	"github.com/wen/opentalon/internal/simulator"
	"github.com/wen/opentalon/internal/simulatorhttp"
	"github.com/wen/opentalon/internal/storage"
	"github.com/wen/opentalon/internal/workflow"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExternalDatasetProvenanceUsesLoadedDeclaration(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "directory-is-not-version")
	require.NoError(t, os.CopyFS(root, os.DirFS(testDatasetRoot(t))))
	require.NoError(t, os.WriteFile(filepath.Join(root, "dataset.yaml"), []byte("version: declared-server-build\n"), 0600))
	ds, e := scenario.LoadDataset(root)
	require.NoError(t, e)
	item, ok := ds.Find("mapping-regression-rollback-001")
	require.True(t, ok)
	sim, e := simulator.New(item.Scenario)
	require.NoError(t, e)
	require.NoError(t, sim.Advance(ctx, 5*time.Minute))
	s, e := simulatorhttp.NewServer(sim, ds.Version)
	require.NoError(t, e)
	h := httptest.NewServer(s)
	defer h.Close()
	client, e := simulatorhttp.NewClient(h.URL, time.Second)
	require.NoError(t, e)
	info, e := client.Info(ctx)
	require.NoError(t, e)
	require.Equal(t, "declared-server-build", info.DatasetVersion)
	db, e := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "metadata.db"))
	require.NoError(t, e)
	defer db.Close()
	calls := 0
	cfg := ConnectedConfig{Config: Config{Storage: db, DatasetRoot: "data/toolops-v1", ScenarioID: info.IncidentID, RunConfig: runmeta.Config{EnvironmentID: info.EnvironmentID}, InvestigatorFactory: func(flow *workflow.IncidentWorkflow, _ platform.ToolOpsPlatform) (controller.Investigator, error) {
		calls++
		return &intentInvestigator{flow: flow, incidentID: info.IncidentID}, nil
	}}, Connector: client}
	cfg.Provenance.DatasetVersion = "conflicting-local-version"
	_, e = RunConnected(ctx, cfg)
	require.ErrorIs(t, e, platform.ErrConflict)
	require.Zero(t, calls)
	cfg.Provenance.DatasetVersion = ""
	out, e := RunConnected(ctx, cfg)
	require.NoError(t, e)
	require.Equal(t, controller.StopAwaitingApproval, out.Reason)
	runID := out.Snapshot.IntentIDPrefix
	before, e := db.Checkpoints().Get(ctx, runID)
	require.NoError(t, e)
	require.Equal(t, ds.Version, before.Provenance.DatasetVersion)
	artifact, e := db.RunArtifacts().Get(ctx, runID)
	require.NoError(t, e)
	require.Equal(t, ds.Version, artifact.Provenance.DatasetVersion)
	// A new instance with identical data cannot replace the original environment.
	replacement, e := simulatorhttp.NewServer(sim, ds.Version)
	require.NoError(t, e)
	other := httptest.NewServer(replacement)
	defer other.Close()
	otherClient, e := simulatorhttp.NewClient(other.URL, time.Second)
	require.NoError(t, e)
	bad := cfg
	bad.Connector = otherClient
	_, e = Resume(ctx, ResumeConfig{ConnectedConfig: bad, RunID: runID})
	require.ErrorIs(t, e, platform.ErrEnvironmentMismatch)
	bad.Connector = metadataOverride{Client: client, version: "changed-server-version"}
	_, e = Resume(ctx, ResumeConfig{ConnectedConfig: bad, RunID: runID})
	require.ErrorIs(t, e, platform.ErrConflict)
	unchanged, e := db.Checkpoints().Get(ctx, runID)
	require.NoError(t, e)
	require.Equal(t, before, unchanged)
	cfg.Provenance.DatasetVersion = "current-local-version-must-not-rewrite-history"
	cfg.InvestigatorFactory = func(flow *workflow.IncidentWorkflow, _ platform.ToolOpsPlatform) (controller.Investigator, error) {
		return &resumeInvestigator{flow: flow, call: func(context.Context, string) error {
			_, err := flow.Apply(workflow.Event{Type: workflow.EventEscalated, Actor: workflow.ActorAgent, Reason: "fresh investigation"})
			return err
		}}, nil
	}
	resumed, e := Resume(ctx, ResumeConfig{ConnectedConfig: cfg, RunID: runID})
	require.NoError(t, e)
	require.False(t, resumed.Waiting)
	after, e := db.Checkpoints().Get(ctx, runID)
	require.NoError(t, e)
	require.Equal(t, before.Provenance, after.Provenance)
	require.Equal(t, before.RunConfig.EnvironmentID, after.RunConfig.EnvironmentID)
	artifact, e = db.RunArtifacts().Get(ctx, runID)
	require.NoError(t, e)
	require.Equal(t, ds.Version, artifact.Provenance.DatasetVersion)
}

type metadataOverride struct {
	*simulatorhttp.Client
	version string
}

func (c metadataOverride) EnvironmentMetadata(ctx context.Context) (platform.EnvironmentMetadata, error) {
	m, e := c.Client.EnvironmentMetadata(ctx)
	m.DatasetVersion = c.version
	return m, e
}
