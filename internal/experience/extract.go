package experience

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/wen/opentalon/internal/runartifact"
	"github.com/wen/opentalon/internal/runmeta"
	"github.com/wen/opentalon/internal/workflow"
)

type Evidence struct {
	Ref     string          `json:"ref"`
	Kind    string          `json:"kind"`
	Content json.RawMessage `json:"content"`
}
type Finding struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Status   string `json:"status"`
}
type Assessment struct {
	Version      string    `json:"evaluator_version"`
	Verdict      string    `json:"verdict"`
	Checks       []Finding `json:"checks"`
	ResultDigest string    `json:"result_digest"`
}
type Snapshot struct {
	ArtifactDigest string             `json:"artifact_digest"`
	Provenance     runmeta.Provenance `json:"provenance"`
	RunConfig      runmeta.Config     `json:"run_config"`
	Assessment     *Assessment        `json:"assessment,omitempty"`
	RunID          string             `json:"run_id"`
	ScenarioID     string             `json:"scenario_id"`
	DatasetVersion string             `json:"dataset_version"`
	Outcome        string             `json:"outcome"`
	Evidence       []Evidence         `json:"evidence"`
}

// Freeze stores only public observations and explicit historical hypotheses, never scenario expectations.
func Freeze(ctx context.Context, s Store, a runartifact.RunArtifact, assessment ...Assessment) (Snapshot, error) {
	if a.RunID == "" || (a.Outcome != "completed" && a.Outcome != "failed") {
		return Snapshot{}, errors.New("only terminal runs may be extracted")
	}
	v := Snapshot{ArtifactDigest: Digest(a), Provenance: a.Provenance, RunConfig: a.RunConfig, RunID: a.RunID, ScenarioID: a.ScenarioID, DatasetVersion: a.Provenance.DatasetVersion, Outcome: a.Outcome}
	if len(assessment) > 0 {
		v.Assessment = &assessment[0]
	}
	add := func(ref, kind string, x any) {
		if ref == "" {
			return
		}
		b, _ := json.Marshal(x)
		v.Evidence = append(v.Evidence, Evidence{ref, kind, Redact(b)})
	}
	for _, r := range a.AgentRuns {
		for _, c := range r.ToolCalls {
			if c.Action == workflow.AgentActionRead {
				ref := c.EvidenceRef
				if ref == "" {
					ref = c.CallID
				}
				add(ref, "public_tool_observation", c)
			}
		}
	}
	for _, o := range a.Operations {
		add(o.ID, "operation_result", o)
	}
	for _, i := range a.ExecutionIntents {
		add(i.ID, "historical_hypothesis_not_fact", i)
	}
	return v, Save(ctx, s, "snapshot", Digest(v), v)
}

// Redact structured secret fields and Simulator mechanics before sending data to a model.
func Redact(raw json.RawMessage) json.RawMessage {
	var v any
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	if d.Decode(&v) != nil {
		return json.RawMessage(`null`)
	}
	var visit func(any) any
	visit = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				l := strings.ToLower(k)
				if strings.Contains(l, "secret") || strings.Contains(l, "password") || strings.Contains(l, "token") || strings.Contains(l, "api_key") || l == "authorization" || l == "internal_effect" || l == "expectations" {
					x[k] = "[REDACTED]"
				} else {
					x[k] = visit(val)
				}
			}
		case []any:
			for i := range x {
				x[i] = visit(x[i])
			}
		}
		return v
	}
	b, _ := json.Marshal(visit(v))
	return b
}

func resolveSources(c *Card, ss []Snapshot) error {
	for i, src := range c.Sources {
		found := false
		for _, s := range ss {
			if s.RunID != src.RunID {
				continue
			}
			for _, ref := range src.EvidenceRefs {
				ok := false
				for _, ev := range s.Evidence {
					if ev.Ref == ref {
						ok = true
						break
					}
				}
				if !ok {
					return fmt.Errorf("unknown evidence reference %q", ref)
				}
			}
			c.Sources[i] = Source{s.RunID, s.ScenarioID, s.DatasetVersion, Digest(s), src.EvidenceRefs}
			found = true
			break
		}
		if !found {
			return errors.New("unknown source run")
		}
	}
	return nil
}
func DecodeStrict(b []byte, v any) error {
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	var trailing any
	if e := d.Decode(&trailing); e != io.EOF {
		return errors.New("trailing JSON content")
	}
	return nil
}

// The source scenario IDs often encode answers; only use them for provenance and
// offline test exclusion, never as extraction-model input.
func extractionInput(snapshots []Snapshot) any {
	type input struct {
		RunID      string      `json:"run_id"`
		Outcome    string      `json:"outcome"`
		Evidence   []Evidence  `json:"evidence"`
		Assessment *Assessment `json:"assessment,omitempty"`
	}
	out := []input{}
	for _, s := range snapshots {
		out = append(out, input{s.RunID, s.Outcome, s.Evidence, s.Assessment})
	}
	return out
}
