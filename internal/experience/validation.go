package experience

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// Case is offline-only metadata. Cause and expectations never enter retrieval.
type Case struct {
	ID          string   `json:"id"`
	DatasetRoot string   `json:"dataset_root"`
	ScenarioID  string   `json:"scenario_id"`
	Lineage     string   `json:"lineage"`
	Symptoms    []string `json:"symptoms"`
	ContextTags []string `json:"context_tags"`
	CauseFamily string   `json:"cause_family"`
	Regression  bool     `json:"regression"`
}
type Selection struct {
	Case    Case     `json:"case"`
	Reasons []string `json:"reasons"`
}
type Plan struct {
	Release string      `json:"release"`
	Cases   []Selection `json:"cases"`
	Gaps    []string    `json:"gaps"`
}

func Select(r Release, catalog []Case) Plan {
	p := Plan{Release: r.ID, Cases: []Selection{}, Gaps: []string{}}
	excluded := map[string]bool{}
	lineages := map[string]bool{}
	for _, c := range r.Cards {
		for _, src := range c.Sources {
			excluded[src.ScenarioID] = true
		}
	}
	for _, c := range catalog {
		if excluded[c.ScenarioID] && c.Lineage != "" {
			lineages[c.Lineage] = true
		}
	}
	for source := range excluded {
		found := false
		for _, c := range catalog {
			if c.ScenarioID == source {
				found = true
				break
			}
		}
		if !found {
			p.Gaps = append(p.Gaps, "missing source lineage metadata: "+source)
		}
	}
	selected := map[string]*Selection{}
	add := func(c Case, reason string) {
		v := selected[c.ID]
		if v == nil {
			v = &Selection{Case: c}
			selected[c.ID] = v
		}
		v.Reasons = append(v.Reasons, reason)
	}
	for _, card := range r.Cards {
		positive, negative := false, false
		for _, c := range catalog {
			if excluded[c.ScenarioID] || lineages[c.Lineage] {
				continue
			}
			if !overlap(card.Symptoms, c.Symptoms) {
				continue
			}
			if c.CauseFamily == card.CauseFamily {
				if len(card.ContextTags) > 0 && !containsAll(c.ContextTags, card.ContextTags) {
					continue
				}
				positive = true
				add(c, "positive:"+card.Key())
			} else {
				negative = true
				add(c, "counterexample:"+card.Key())
			}
		}
		if !positive {
			p.Gaps = append(p.Gaps, "missing independent positive for "+card.Key())
		}
		if !negative {
			p.Gaps = append(p.Gaps, "missing same-symptom counterexample for "+card.Key())
		}
	}
	regressions := 0
	for _, c := range catalog {
		if c.Regression && !excluded[c.ScenarioID] && !lineages[c.Lineage] {
			add(c, "regression")
			regressions++
		}
	}
	if regressions == 0 {
		p.Gaps = append(p.Gaps, "missing independent regression cases")
	}
	for _, v := range selected {
		p.Cases = append(p.Cases, *v)
	}
	sort.Slice(p.Cases, func(i, j int) bool { return p.Cases[i].Case.ID < p.Cases[j].Case.ID })
	return p
}
func overlap(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}
func containsAll(a, b []string) bool {
	for _, v := range b {
		if !overlap(a, []string{v}) {
			return false
		}
	}
	return true
}

// ChangedCards limits targeted validation to the batch, while fixed regression
// cases still exercise the complete release.
func ChangedCards(r Release, parent Release) []Card {
	old := map[string]string{}
	for _, c := range parent.Cards {
		old[c.Key()] = Digest(c)
	}
	out := []Card{}
	for _, c := range r.Cards {
		if old[c.Key()] != Digest(c) {
			out = append(out, c)
		}
	}
	return out
}
func (s *Service) Plan(ctx context.Context, r Release, catalog []Case) (Plan, error) {
	parent := Release{}
	var e error
	if r.Parent != "" {
		parent, e = Load[Release](ctx, s.Store, "release", r.Parent)
		if e != nil {
			return Plan{}, e
		}
	}
	selected := r
	selected.Cards = ChangedCards(r, parent)
	p := Select(selected, catalog)
	if len(selected.Cards) == 0 {
		p.Gaps = append(p.Gaps, "no changed candidates")
	}
	return p, nil
}

type Trial struct {
	CaseID           string `json:"case_id"`
	Repeat           int    `json:"repeat"`
	Arm              string `json:"arm"`
	RunID            string `json:"run_id"`
	Passed           bool   `json:"passed"`
	SafetyFailures   int    `json:"safety_failures"`
	ModelCalls       int    `json:"model_calls"`
	Tokens           int    `json:"tokens"`
	Retrieved        int    `json:"retrieved"`
	ArtifactDigest   string `json:"artifact_digest"`
	EvaluationDigest string `json:"evaluation_digest"`
}
type Runner interface {
	Run(context.Context, Case, string, string, int) (Trial, error)
	Fingerprint(context.Context, []Selection) (string, error)
}
type Policy struct {
	Repeats       int     `json:"repeats"`
	MaxTokenRatio float64 `json:"max_token_ratio"`
}
type Report struct {
	ID                string   `json:"id"`
	Release           string   `json:"release"`
	Baseline          string   `json:"baseline"`
	ReleaseDigest     string   `json:"release_digest"`
	RunnerFingerprint string   `json:"runner_fingerprint"`
	Plan              Plan     `json:"plan"`
	Policy            Policy   `json:"policy"`
	Trials            []Trial  `json:"trials"`
	Passed            bool     `json:"passed"`
	Reasons           []string `json:"reasons"`
}

func (s *Service) Validate(ctx context.Context, id string, catalog []Case, policy Policy, runner Runner) (report Report, err error) {
	r, e := Load[Release](ctx, s.Store, "release", id)
	if e != nil {
		return report, e
	}
	if e = s.checkIndex(ctx, r); e != nil {
		return report, e
	}
	if policy.Repeats < 2 || policy.Repeats > 20 || policy.MaxTokenRatio < 1 || policy.MaxTokenRatio > 10 {
		return report, errors.New("policy requires 2..20 repeats and token ratio in [1,10]")
	}
	ids := map[string]bool{}
	for _, c := range catalog {
		if c.ID == "" || c.ScenarioID == "" || c.DatasetRoot == "" || c.CauseFamily == "" || c.Lineage == "" || ids[c.ID] {
			return report, errors.New("catalog requires unique IDs, roots, scenarios, cause families and lineage")
		}
		ids[c.ID] = true
	}
	plan, e := s.Plan(ctx, r, catalog)
	if e != nil {
		return report, e
	}
	report = Report{Release: id, Baseline: r.Parent, ReleaseDigest: Digest(r), Plan: plan, Policy: policy, Trials: []Trial{}, Reasons: []string{}}
	defer func() {
		if err != nil {
			report.Reasons = append(report.Reasons, err.Error())
			report.Passed = false
		}
		report.ID = Digest(report)
		e := Save(context.WithoutCancel(ctx), s.Store, "report", report.ID, report)
		if err == nil {
			err = e
		}
	}()
	if len(report.Plan.Gaps) > 0 {
		report.Reasons = append(report.Reasons, report.Plan.Gaps...)
		return report, nil
	}
	report.RunnerFingerprint, e = runner.Fingerprint(ctx, report.Plan.Cases)
	if e != nil {
		return report, e
	}
	for _, selection := range report.Plan.Cases {
		for n := 0; n < policy.Repeats; n++ {
			arms := []string{"baseline", "direct", "retrieval"}
			if n%2 == 1 {
				arms = []string{"retrieval", "direct", "baseline"}
			}
			for _, arm := range arms {
				release := id
				if arm == "baseline" {
					release = r.Parent
				}
				t, e := runner.Run(ctx, selection.Case, release, arm, n)
				if e != nil {
					return report, e
				}
				t.CaseID = selection.Case.ID
				t.Arm = arm
				t.Repeat = n
				report.Trials = append(report.Trials, t)
			}
		}
	}
	fingerprint, e := runner.Fingerprint(ctx, report.Plan.Cases)
	if e != nil {
		return report, e
	}
	if fingerprint != report.RunnerFingerprint {
		return report, errors.New("runner or dataset changed during validation")
	}
	report.Reasons = assess(report)
	report.Passed = len(report.Reasons) == 0
	return report, nil
}
func assess(r Report) []string {
	reasons := []string{}
	type counts struct{ pass, calls, tokens, hits int }
	groups := map[string]map[string]counts{}
	totals := map[string]counts{}
	for _, t := range r.Trials {
		if groups[t.CaseID] == nil {
			groups[t.CaseID] = map[string]counts{}
		}
		v := groups[t.CaseID][t.Arm]
		if t.Passed {
			v.pass++
		}
		v.calls += t.ModelCalls
		v.tokens += t.Tokens
		v.hits += t.Retrieved
		groups[t.CaseID][t.Arm] = v
		sum := totals[t.Arm]
		if t.Passed {
			sum.pass++
		}
		sum.calls += t.ModelCalls
		sum.tokens += t.Tokens
		sum.hits += t.Retrieved
		totals[t.Arm] = sum
		if t.Arm != "baseline" && t.SafetyFailures > 0 {
			reasons = append(reasons, fmt.Sprintf("safety failure: %s/%s", t.CaseID, t.Arm))
		}
	}
	for id, g := range groups {
		for _, arm := range []string{"direct", "retrieval"} {
			if g[arm].pass < g["baseline"].pass {
				reasons = append(reasons, "success regression: "+id+"/"+arm)
			}
		}
	}
	b := totals["baseline"]
	for _, arm := range []string{"direct", "retrieval"} {
		c := totals[arm]
		if c.pass < b.pass || (c.pass == b.pass && c.calls >= b.calls) {
			reasons = append(reasons, "no demonstrated success or call-efficiency gain: "+arm)
		}
		if float64(c.tokens) > float64(max(1, b.tokens))*r.Policy.MaxTokenRatio {
			reasons = append(reasons, "token budget regression: "+arm)
		}
	}
	if totals["retrieval"].hits == 0 {
		reasons = append(reasons, "no experience activated during retrieval trials")
	}
	return reasons
}
