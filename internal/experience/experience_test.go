package experience_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/experience"
	"github.com/wen/opentalon/internal/runartifact"
	"github.com/wen/opentalon/internal/storage"
	"github.com/wen/opentalon/internal/workflow"
)

type index struct {
	fail   bool
	builds int
	keys   []string
}

func (i *index) Profile() string { return "profile" }
func (i *index) Build(context.Context, experience.Release) error {
	i.builds++
	if i.fail {
		return errors.New("offline")
	}
	return nil
}
func (i *index) Search(context.Context, experience.Release, experience.Query) ([]string, error) {
	return i.keys, nil
}

type ranker struct{}

func (ranker) Rank(_ context.Context, _ string, c []experience.Card) ([]int, error) {
	v := []int{}
	for i := range c {
		v = append(v, i)
	}
	return v, nil
}

type modelStub struct {
	outputs []string
	calls   int
}

func (m *modelStub) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	v := m.outputs[min(m.calls, len(m.outputs)-1)]
	m.calls++
	return schema.AssistantMessage(v, nil), nil
}
func (m *modelStub) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("unused")
}
func setup(t *testing.T) (context.Context, *experience.Service, experience.Card) {
	t.Helper()
	ctx := context.Background()
	db, e := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "db"))
	require.NoError(t, e)
	t.Cleanup(func() { db.Close() })
	s := &experience.Service{Store: db.Experiences(), Index: &index{}, Ranker: ranker{}}
	snap, e := experience.Freeze(ctx, s.Store, runartifact.RunArtifact{RunID: "source", ScenarioID: "training", Outcome: "failed", AgentRuns: []runartifact.AgentRun{{ToolCalls: []runartifact.ToolCall{{CallID: "ev", EvidenceRef: "ev", Action: workflow.AgentActionRead, Output: json.RawMessage(`{"password":"hidden","status":"timeout"}`)}}}}})
	require.NoError(t, e)
	c := experience.Card{Schema: experience.Schema, ID: "exp_a", Version: 1, Title: "check connection", Component: "connection_pool", Symptoms: []string{"timeout"}, CauseFamily: "stale", ApplicableWhen: []string{"timeout after change"}, Checks: []experience.Check{{Observe: "connection version", Supports: "old", RulesOut: "new"}}, Verification: "probe", Exceptions: []string{"upstream unavailable"}, Sources: []experience.Source{{RunID: "source", ScenarioID: "training", SnapshotDigest: experience.Digest(snap), EvidenceRefs: []string{"ev"}}}}
	require.NoError(t, experience.Save(ctx, s.Store, "candidate", c.Key(), c))
	return ctx, s, c
}
func review(t *testing.T, ctx context.Context, s *experience.Service, c experience.Card, parent string) {
	t.Helper()
	m := &modelStub{outputs: []string{`{"supported":true,"reasons":["public evidence supports conditional check"],"conflicts":[]}`}}
	_, e := experience.ReviewCandidate(ctx, s.Store, m, "reviewer", c.Key(), parent)
	require.NoError(t, e)
}
func TestReleaseGateRetryRollbackAndPinnedReader(t *testing.T) {
	ctx, s, c := setup(t)
	_, e := s.Stage(ctx, "v1", "", []string{c.Key()})
	require.ErrorContains(t, e, "review")
	review(t, ctx, s, c, "")
	r, e := s.Stage(ctx, "v1", "", []string{c.Key()})
	require.NoError(t, e)
	_, e = s.Open(ctx, "v1", false)
	require.Error(t, e)
	idx := s.Index.(*index)
	idx.fail = true
	require.Error(t, s.IndexRelease(ctx, "v1"))
	_, e = s.Open(ctx, "v1", true)
	require.Error(t, e)
	idx.fail = false
	require.NoError(t, s.IndexRelease(ctx, "v1"))
	require.NoError(t, s.IndexRelease(ctx, "v1"))
	require.Equal(t, 2, idx.builds)
	require.Error(t, s.Publish(ctx, "v1", "missing"))
	report := experience.Report{Release: "v1", ReleaseDigest: experience.Digest(r), Passed: true}
	require.NoError(t, experience.Save(ctx, s.Store, "report", "passing", report))
	require.NoError(t, s.Publish(ctx, "v1", "passing"))
	pinned, e := s.Open(ctx, "v1", false)
	require.NoError(t, e)
	idx.keys = []string{c.Key(), c.Key(), "foreign@1"}
	result, e := pinned.Search(ctx, experience.Query{Query: "timeout", Limit: 100})
	require.NoError(t, e)
	require.Len(t, result.Items, 1)
	raw, e := pinned.Evidence(ctx, c.ID, 1, "source", "ev")
	require.NoError(t, e)
	require.NotContains(t, string(raw), "hidden")
	require.Contains(t, string(raw), "REDACTED")
	_, e = pinned.Evidence(ctx, c.ID, 1, "source", "not-cited")
	require.ErrorIs(t, e, experience.ErrNotFound)
	c.Version = 2
	c.Title = "refined"
	require.NoError(t, experience.Save(ctx, s.Store, "candidate", c.Key(), c))
	review(t, ctx, s, c, "v1")
	r2, e := s.Stage(ctx, "v2", "v1", []string{c.Key()})
	require.NoError(t, e)
	require.NoError(t, s.IndexRelease(ctx, "v2"))
	require.NoError(t, experience.Save(ctx, s.Store, "report", "second", experience.Report{Release: "v2", Baseline: "v1", ReleaseDigest: experience.Digest(r2), Passed: true}))
	require.NoError(t, s.Publish(ctx, "v2", "second"))
	old, e := pinned.Get(ctx, c.ID, 1)
	require.NoError(t, e)
	require.Equal(t, 1, old.Version)
	require.ErrorIs(t, s.Rollback(ctx, "v1", "wrong"), experience.ErrConflict)
	require.NoError(t, s.Rollback(ctx, "v1", "v2"))
	active, e := s.Active(ctx)
	require.NoError(t, e)
	require.Equal(t, "v1", active)
	c.Title = "mutated"
	require.ErrorIs(t, experience.Save(ctx, s.Store, "candidate", c.Key(), c), experience.ErrConflict)
}
func TestExtractionRejectsFabricatedReferencesAndAbstains(t *testing.T) {
	ctx, s, c := setup(t)
	snap, e := experience.Load[experience.Snapshot](ctx, s.Store, "snapshot", c.Sources[0].SnapshotDigest)
	require.NoError(t, e)
	bad := c
	bad.Sources[0].EvidenceRefs = []string{"invented"}
	m := &extractionModel{messages: []*schema.Message{candidateCall(bad)}}
	r, e := experience.Extract(ctx, s.Store, m, "test", []experience.Snapshot{snap})
	require.ErrorContains(t, e, "unknown evidence")
	require.Equal(t, 2, m.calls)
	require.Empty(t, r.Cards)
	m = &extractionModel{messages: []*schema.Message{finishCall("insufficient evidence")}}
	r, e = experience.Extract(ctx, s.Store, m, "test", []experience.Snapshot{snap})
	require.NoError(t, e)
	require.Empty(t, r.Cards)
}
func TestSelectionExcludesTrainingLineageAndRequiresCounterexamples(t *testing.T) {
	_, _, c := setup(t)
	r := experience.Release{ID: "v1", Cards: []experience.Card{c}}
	catalog := []experience.Case{{ID: "source", ScenarioID: "training", Lineage: "train"}, {ID: "renamed", ScenarioID: "renamed", Lineage: "train", Symptoms: []string{"timeout"}, CauseFamily: "stale"}, {ID: "positive", ScenarioID: "new", Lineage: "new", Symptoms: []string{"timeout"}, CauseFamily: "stale"}, {ID: "negative", ScenarioID: "other", Lineage: "other", Symptoms: []string{"timeout"}, CauseFamily: "upstream", Regression: true}}
	p := experience.Select(r, catalog)
	require.Empty(t, p.Gaps)
	require.Len(t, p.Cases, 2)
	p = experience.Select(r, catalog[:3])
	require.NotEmpty(t, p.Gaps)
}

type trials struct{ unsafe bool }

func (t trials) Fingerprint(context.Context, []experience.Selection) (string, error) {
	return "fixed", nil
}
func (t trials) Run(_ context.Context, _ experience.Case, _ string, arm string, _ int) (experience.Trial, error) {
	v := experience.Trial{RunID: "trial", Passed: true, Tokens: 100, ModelCalls: 5, ArtifactDigest: "a", EvaluationDigest: "e"}
	if arm != "baseline" {
		v.ModelCalls = 3
		v.Retrieved = 1
		if t.unsafe {
			v.SafetyFailures = 1
		}
	}
	return v, nil
}
func TestBatchValidationCannotPublishSafetyRegression(t *testing.T) {
	ctx, s, c := setup(t)
	review(t, ctx, s, c, "")
	_, e := s.Stage(ctx, "v1", "", []string{c.Key()})
	require.NoError(t, e)
	require.NoError(t, s.IndexRelease(ctx, "v1"))
	catalog := []experience.Case{{ID: "source", DatasetRoot: "data", ScenarioID: "training", Lineage: "training", CauseFamily: "stale"}, {ID: "pos", DatasetRoot: "data", ScenarioID: "pos", Lineage: "pos", Symptoms: []string{"timeout"}, CauseFamily: "stale"}, {ID: "neg", DatasetRoot: "data", ScenarioID: "neg", Lineage: "neg", Symptoms: []string{"timeout"}, CauseFamily: "upstream", Regression: true}}
	r, e := s.Validate(ctx, "v1", catalog, experience.Policy{Repeats: 2, MaxTokenRatio: 1.2}, trials{unsafe: true})
	require.NoError(t, e)
	require.False(t, r.Passed)
	require.Error(t, s.Publish(ctx, "v1", r.ID))
	r, e = s.Validate(ctx, "v1", catalog, experience.Policy{Repeats: 2, MaxTokenRatio: 1.2}, trials{})
	require.NoError(t, e)
	require.True(t, r.Passed)
	require.Len(t, r.Trials, 12)
	require.NoError(t, s.Publish(ctx, "v1", r.ID))
}

func TestSuccessfulExtractionResolvesSourceAndCanBeReviewed(t *testing.T) {
	ctx, s, c := setup(t)
	snap, e := experience.Load[experience.Snapshot](ctx, s.Store, "snapshot", c.Sources[0].SnapshotDigest)
	require.NoError(t, e)
	c.ID = ""
	c.Schema = ""
	c.Version = 0
	c.Sources = []experience.Source{{RunID: "source", EvidenceRefs: []string{"ev"}}}
	m := &extractionModel{messages: []*schema.Message{candidateCall(c), finishCall("complete")}}
	result, e := experience.Extract(ctx, s.Store, m, "test", []experience.Snapshot{snap})
	require.NoError(t, e)
	require.Len(t, result.Cards, 1)
	saved, e := experience.Load[experience.Card](ctx, s.Store, "candidate", result.Cards[0])
	require.NoError(t, e)
	require.NoError(t, experience.CheckSources(ctx, s.Store, saved))
	require.Equal(t, 1, saved.Version)
	require.Equal(t, experience.Schema, saved.Schema)
	require.Equal(t, "training", saved.Sources[0].ScenarioID)
}

func TestChangedBatchNeedsPeerReviewAndMissingCoverageFailsClosed(t *testing.T) {
	ctx, s, c := setup(t)
	review(t, ctx, s, c, "")
	d := c
	d.ID = "exp_b"
	d.Checks = []experience.Check{{Observe: "upstream", Supports: "down", RulesOut: "healthy"}}
	require.NoError(t, experience.Save(ctx, s.Store, "candidate", d.Key(), d))
	review(t, ctx, s, d, "")
	_, e := s.Stage(ctx, "batch", "", []string{c.Key(), d.Key()})
	require.ErrorContains(t, e, "review required")
	_, e = s.Stage(ctx, "v1", "", []string{c.Key()})
	require.NoError(t, e)
	require.NoError(t, s.IndexRelease(ctx, "v1"))
	report, e := s.Validate(ctx, "v1", nil, experience.Policy{Repeats: 2, MaxTokenRatio: 1.2}, trials{})
	require.NoError(t, e)
	require.False(t, report.Passed)
	require.NotEmpty(t, report.Plan.Gaps)
	require.Empty(t, report.Trials)
	require.Error(t, s.Publish(ctx, "v1", report.ID))
}
