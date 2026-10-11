package experienceeval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wen/opentalon/internal/scenario"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/app"
	"github.com/wen/opentalon/internal/controller"
	"github.com/wen/opentalon/internal/experience"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/runmeta"
	"github.com/wen/opentalon/internal/workflow"
)

type failingInvestigator struct{ id string }

func (f failingInvestigator) IncidentID() string { return f.id }
func (f failingInvestigator) Investigate(context.Context, string) error {
	return errors.New("intentional validation failure")
}
func TestRunnerUsesIndependentWorldsAndRealEvaluator(t *testing.T) {
	worlds := []string{}
	r := &Runner{OutputDir: t.TempDir(), EvaluatorDir: "../../evaluator/src", Timeout: 20 * time.Second, Config: app.Config{Provenance: runmeta.Provenance{CodeVersion: "test"}, InvestigatorFactory: func(w *workflow.IncidentWorkflow, p platform.ToolOpsPlatform) (controller.Investigator, error) {
		worlds = append(worlds, fmt.Sprintf("%p", p))
		return failingInvestigator{w.Snapshot().IncidentID}, nil
	}}}
	c := experience.Case{ID: "test", DatasetRoot: "../../data/toolops-v1", ScenarioID: "mapping-regression-rollback-001"}
	a, e := r.Run(context.Background(), c, "", "baseline", 0)
	require.NoError(t, e)
	b, e := r.Run(context.Background(), c, "", "baseline", 1)
	require.NoError(t, e)
	require.NotEqual(t, a.RunID, b.RunID)
	require.NotEqual(t, worlds[0], worlds[1])
	require.False(t, a.Passed)
	require.NotEmpty(t, a.EvaluationDigest)
	files, e := filepath.Glob(filepath.Join(r.OutputDir, "trial-*", "evaluation.json"))
	require.NoError(t, e)
	require.Len(t, files, 2)
	for _, f := range files {
		data, e := os.ReadFile(f)
		require.NoError(t, e)
		require.Contains(t, string(data), "evaluator_version")
	}
}

func TestShippedCatalogReferencesExistingScenarios(t *testing.T) {
	b, e := os.ReadFile("../../docs/experience/catalog.toolops-v2.json")
	require.NoError(t, e)
	var catalog struct {
		Cases []experience.Case `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(b, &catalog))
	require.Len(t, catalog.Cases, 15)
	for _, c := range catalog.Cases {
		d, e := scenario.LoadDataset(filepath.Join("../..", c.DatasetRoot))
		require.NoError(t, e)
		_, ok := d.Find(c.ScenarioID)
		require.True(t, ok, c.ScenarioID)
		require.NotEmpty(t, c.Lineage)
		require.NotEmpty(t, c.CauseFamily)
	}
}
