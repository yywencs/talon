package experienceeval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/wen/opentalon/internal/evaluation"
	"github.com/wen/opentalon/internal/experience"
	"github.com/wen/opentalon/internal/runartifact"
	"github.com/wen/opentalon/internal/scenario"
)

// Assess supplies only verdicts and check identifiers to extraction, not expected
// answers, root-cause annotations, scenario timeline or hidden Simulator state.
func Assess(ctx context.Context, a runartifact.RunArtifact, datasetRoot, python, packageRoot string) (experience.Assessment, error) {
	var out experience.Assessment
	d, e := scenario.LoadDataset(datasetRoot)
	if e != nil {
		return out, e
	}
	if d.Version != a.Provenance.DatasetVersion {
		return out, fmt.Errorf("evaluation dataset does not match source run")
	}
	c, ok := d.Find(a.ScenarioID)
	if !ok {
		return out, fmt.Errorf("source scenario missing")
	}
	input := evaluation.Input{SchemaVersion: evaluation.InputSchemaVersion, Artifact: a, Expectations: c.Expectations}
	b, e := json.Marshal(input)
	if e != nil {
		return out, e
	}
	if python == "" {
		python = "python3"
	}
	cmd := exec.CommandContext(ctx, python, "-c", `import json,sys; from talon_evaluator.core import evaluate; print(json.dumps(evaluate(json.load(sys.stdin))))`)
	cmd.Env = append(os.Environ(), "PYTHONPATH="+packageRoot)
	cmd.Stdin = bytes.NewReader(b)
	raw, e := cmd.Output()
	if e != nil {
		return out, fmt.Errorf("source evaluation: %w", e)
	}
	var result struct {
		Version string `json:"evaluator_version"`
		Summary struct {
			Verdict string `json:"verdict"`
		} `json:"summary"`
		Checks []experience.Finding `json:"checks"`
	}
	if e = json.Unmarshal(raw, &result); e != nil {
		return out, e
	}
	out.Version = result.Version
	out.Verdict = result.Summary.Verdict
	out.Checks = result.Checks
	out.ResultDigest = experience.Digest(json.RawMessage(raw))
	if out.Verdict == "" {
		return out, fmt.Errorf("missing evaluation verdict")
	}
	return out, nil
}
