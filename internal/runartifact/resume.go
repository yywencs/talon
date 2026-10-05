package runartifact

import (
	"fmt"
	"time"
)

// Resume preserves run identity, evidence references and audit history. A usage
// floor from the checkpoint accounts for model calls missing from the last audit.
func Resume(saved RunArtifact, used int) (*Recorder, error) {
	if saved.SchemaVersion != SchemaVersion || saved.RunID == "" || used < 0 {
		return nil, fmt.Errorf("invalid run artifact for resume")
	}
	r := &Recorder{artifact: cloneArtifact(saved), evidence: map[string]struct{}{}, now: time.Now}
	calls := 0
	for _, run := range r.artifact.AgentRuns {
		calls += len(run.ModelCalls)
		for _, call := range run.ToolCalls {
			if call.EvidenceRef != "" {
				r.evidence[call.EvidenceRef] = struct{}{}
			}
		}
	}
	if saved.Summary.ModelCalls > used {
		used = saved.Summary.ModelCalls
	}
	if used > calls {
		r.modelCallOffset = used - calls
	}
	r.artifact.Outcome = "running"
	r.artifact.StopReason = ""
	r.artifact.Failure = nil
	r.artifact.FinishedAt = time.Time{}
	r.artifact.Duration = 0
	return r, nil
}
