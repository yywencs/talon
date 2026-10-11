// Package experienceeval runs isolated Simulator A/B trials using the real app
// and the existing Python evaluator. Scenario truth never enters the Agent input.
package experienceeval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/wen/opentalon/internal/agent"
	"github.com/wen/opentalon/internal/app"
	"github.com/wen/opentalon/internal/evaluation"
	"github.com/wen/opentalon/internal/experience"
	"github.com/wen/opentalon/internal/scenario"
	"github.com/wen/opentalon/internal/storage"
)

type Runner struct {
	Config                          app.Config
	Service                         *experience.Service
	Python, EvaluatorDir, OutputDir string
	Timeout                         time.Duration
}

func (r *Runner) Fingerprint(_ context.Context, cases []experience.Selection) (string, error) {
	prompts, e := agent.LoadPromptSet(r.Config.PromptDirectory)
	if e != nil {
		return "", e
	}
	parts := []string{experience.Digest(r.Config.RunConfig), prompts.Digest, r.Config.Provenance.CodeVersion, fmt.Sprint(r.Config.AgentMaxSteps), fmt.Sprint(r.Timeout)}
	// Bind evaluator source, scenario contents, skill files and compiled runner executable.
	exe, e := os.Executable()
	if e != nil {
		return "", e
	}
	b, e := os.ReadFile(exe)
	if e != nil {
		return "", e
	}
	parts = append(parts, experience.Digest(b))
	roots := []string{r.EvaluatorDir}
	skills := r.Config.SkillDirectory
	if skills == "" {
		skills = "skills"
	}
	if _, e := os.Stat(skills); e == nil {
		roots = append(roots, skills)
	}
	for _, c := range cases {
		d, e := scenario.LoadDataset(c.Case.DatasetRoot)
		if e != nil {
			return "", e
		}
		item, ok := d.Find(c.Case.ScenarioID)
		if !ok {
			return "", fmt.Errorf("scenario %s missing", c.Case.ScenarioID)
		}
		parts = append(parts, experience.Digest(item))
	}
	for _, root := range roots {
		e := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "__pycache__" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, ".pyc") {
				return nil
			}
			b, e := os.ReadFile(path)
			if e == nil {
				parts = append(parts, path, experience.Digest(b))
			}
			return e
		})
		if e != nil {
			return "", e
		}
	}
	return experience.Digest(parts), nil
}
func (r *Runner) Run(ctx context.Context, c experience.Case, release, arm string, repeat int) (experience.Trial, error) {
	var t experience.Trial
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if e := os.MkdirAll(r.OutputDir, 0700); e != nil {
		return t, e
	}
	dir, e := os.MkdirTemp(r.OutputDir, "trial-")
	if e != nil {
		return t, e
	}
	db, e := storage.OpenSQLite(ctx, filepath.Join(dir, "runtime.sqlite"))
	if e != nil {
		return t, e
	}
	defer db.Close()
	cfg := r.Config
	cfg.Storage = db
	cfg.DatasetRoot = c.DatasetRoot
	cfg.ScenarioID = c.ScenarioID
	cfg.AutoApprove = true
	cfg.Provenance.DatasetVersion = ""
	cfg.RunConfig.ExperienceRelease = release
	cfg.RunConfig.ExperienceProfile = ""
	cfg.ExperienceValidation = true
	cfg.DirectExperienceContext = ""
	cfg.Experiences = nil
	if release != "" {
		cfg.Experiences = r.Service
	}
	newKeys := map[string]bool{}
	if release != "" && arm != "baseline" {
		rel, e := experience.Load[experience.Release](ctx, r.Service.Store, "release", release)
		if e != nil {
			return t, e
		}
		parent := experience.Release{}
		if rel.Parent != "" {
			parent, e = experience.Load[experience.Release](ctx, r.Service.Store, "release", rel.Parent)
			if e != nil {
				return t, e
			}
		}
		for _, card := range experience.ChangedCards(rel, parent) {
			newKeys[card.Key()] = true
		}
	}
	if arm == "direct" {
		rel, e := experience.Load[experience.Release](ctx, r.Service.Store, "release", release)
		if e != nil {
			return t, e
		}
		cards := []experience.Card{}
		for _, card := range rel.Cards {
			if newKeys[card.Key()] && matches(card.Symptoms, c.Symptoms) {
				cards = append(cards, card)
			}
		}
		b, _ := json.Marshal(cards)
		if len(b) > 24000 {
			return t, fmt.Errorf("direct candidate context exceeds 24KB; split batch")
		}
		cfg.DirectExperienceContext = string(b)
	}
	result, runErr := app.Run(ctx, cfg)
	if result.Artifact.RunID == "" {
		return t, fmt.Errorf("trial produced no artifact: %w", runErr)
	}
	dataset, e := scenario.LoadDataset(c.DatasetRoot)
	if e != nil {
		return t, e
	}
	item, ok := dataset.Find(c.ScenarioID)
	if !ok {
		return t, fmt.Errorf("scenario missing")
	}
	input := evaluation.Input{SchemaVersion: evaluation.InputSchemaVersion, Artifact: result.Artifact, Expectations: item.Expectations}
	b, _ := json.Marshal(input)
	inputPath := filepath.Join(dir, "input.json")
	if e = os.WriteFile(inputPath, b, 0600); e != nil {
		return t, e
	}
	python := r.Python
	if python == "" {
		python = "python3"
	}
	cmd := exec.CommandContext(ctx, python, "-m", "talon_evaluator", inputPath)
	cmd.Env = append(os.Environ(), "PYTHONPATH="+r.EvaluatorDir)
	out, e := cmd.Output()
	if e != nil {
		exit, ok := e.(*exec.ExitError)
		if !ok || exit.ExitCode() != 1 {
			return t, fmt.Errorf("evaluator invocation failed: %w", e)
		}
	}
	var score struct {
		Summary struct {
			Verdict string `json:"verdict"`
		} `json:"summary"`
		Checks []struct {
			Category string `json:"category"`
			Status   string `json:"status"`
		} `json:"checks"`
	}
	if e = json.Unmarshal(out, &score); e != nil {
		return t, e
	}
	if score.Summary.Verdict == "" {
		return t, fmt.Errorf("missing evaluator verdict")
	}
	if e = os.WriteFile(filepath.Join(dir, "evaluation.json"), out, 0600); e != nil {
		return t, e
	}
	t = experience.Trial{RunID: result.Artifact.RunID, Passed: score.Summary.Verdict == "passed" && runErr == nil, ModelCalls: result.Artifact.Summary.ModelCalls, Tokens: result.Artifact.Summary.TotalTokens, ArtifactDigest: experience.Digest(result.Artifact), EvaluationDigest: experience.Digest(json.RawMessage(out))}
	for _, check := range score.Checks {
		if check.Category == "safety" && check.Status != "passed" {
			t.SafetyFailures++
		}
	}
	for _, run := range result.Artifact.AgentRuns {
		for _, call := range run.ToolCalls {
			if call.Name == "search_experiences" {
				var v struct {
					Data experience.SearchResult `json:"data"`
				}
				if json.Unmarshal(call.Output, &v) == nil {
					for _, item := range v.Data.Items {
						if newKeys[fmt.Sprintf("%s@%d", item.ID, item.Version)] {
							t.Retrieved++
						}
					}
				}
			}
		}
	}
	return t, nil
}
func matches(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}
