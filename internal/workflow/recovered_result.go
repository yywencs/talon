package workflow

import "fmt"

// RecordRecoveredResult attaches a confirmed historical result without executing
// actions or evaluating old checkpoint rules. The caller must verify platform and
// durable execution identities first. Existing output may never be overwritten.
func (w *IncidentWorkflow) RecordRecoveredResult(value ActionResult) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	matched := false
	for _, a := range w.resolvedActions {
		if a.ActionID == value.ActionID && a.IntentID == value.IntentID && a.StageID == value.StageID && a.Digest == value.ActionDigest {
			matched = true
			break
		}
	}
	if !matched || value.OperationID == "" {
		return fmt.Errorf("recovered result has no matching resolved action")
	}
	for _, r := range w.actionResults {
		if r.ActionID == value.ActionID {
			// RecordedAt is observational; compare only the immutable platform result.
			if r.IntentID != value.IntentID || r.StageID != value.StageID || r.ActionDigest != value.ActionDigest || r.OperationID != value.OperationID || r.OperationStatus != value.OperationStatus || !sameSnapshotJSON(r.Output, value.Output) {
				return fmt.Errorf("conflicting recovered result")
			}
			return nil
		}
	}
	w.actionResults = append(w.actionResults, w.sealActionResult(value))
	return nil
}
