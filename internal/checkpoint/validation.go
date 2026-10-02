package checkpoint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wen/opentalon/internal/approval"
	"github.com/wen/opentalon/internal/workflow"
)

// Validate checks checkpoint data invariants at both storage ingress and egress.
func (data Data) Validate() error {
	if data.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported checkpoint schema %q", data.SchemaVersion)
	}
	if strings.TrimSpace(data.RunID) == "" || strings.TrimSpace(data.Workflow.IncidentID) == "" ||
		strings.TrimSpace(data.Workflow.IntentIDPrefix) == "" || data.ModelCallsUsed < 0 {
		return fmt.Errorf("checkpoint requires run identity, intent prefix and nonnegative usage")
	}
	switch data.Boundary {
	case Created:
		if data.Workflow.State != workflow.StateProtected || data.Workflow.ExecutionIntent != nil {
			return fmt.Errorf("created checkpoint requires a protected workflow without an intent")
		}
	case IntentAccepted:
		if data.Workflow.State != workflow.StateValidating || data.Workflow.ExecutionIntent == nil {
			return fmt.Errorf("intent checkpoint requires a validating workflow with an intent")
		}
	case AwaitingApproval:
		if data.Workflow.State != workflow.StateAwaitingApproval || data.Workflow.ExecutionIntent == nil {
			return fmt.Errorf("approval checkpoint requires an awaiting_approval workflow with an intent")
		}
	default:
		return fmt.Errorf("unsupported checkpoint boundary %q", data.Boundary)
	}
	return nil
}

// ValidateApprovals binds the complete request set to the frozen current actions and policies.
// Storage calls this before opening the atomic approval/checkpoint transaction.
func (data Data) ValidateApprovals(requests []approval.Request) error {
	snapshot := data.Workflow
	if snapshot.ExecutionIntent == nil || len(requests) == 0 {
		return fmt.Errorf("approval checkpoint requires an intent and approval requests")
	}
	byAction := make(map[string]approval.Request, len(requests))
	for _, request := range requests {
		if _, exists := byAction[request.ActionID]; exists {
			return fmt.Errorf("duplicate checkpoint approval action %q", request.ActionID)
		}
		byAction[request.ActionID] = request
	}
	actions := workflow.ExecutableActions(snapshot)
	for _, policy := range snapshot.ActionPolicies {
		if policy.Outcome != workflow.ActionPolicyApprovalRequired {
			continue
		}
		request, ok := byAction[policy.ActionID]
		if !ok || request.ID != approval.RequestID(policy.ActionID) || request.IncidentID != snapshot.IncidentID ||
			request.IntentID != snapshot.ExecutionIntent.ID || request.ActionDigest != policy.ActionDigest ||
			request.DryRunOperationID != policy.DryRunOperationID || request.Risk != policy.Risk || request.PolicyReason != policy.Reason {
			return fmt.Errorf("approval request does not match checkpoint policy %q", policy.ActionID)
		}
		matched := false
		for _, action := range actions {
			if action.ID != request.ActionID {
				continue
			}
			want, err := json.Marshal(action.Arguments)
			got, gotErr := json.Marshal(request.Arguments)
			matched = err == nil && gotErr == nil && bytes.Equal(want, got) && action.ToolName == request.ToolName && action.Digest == request.ActionDigest
		}
		if !matched {
			return fmt.Errorf("approval request does not match checkpoint action %q", policy.ActionID)
		}
		delete(byAction, policy.ActionID)
	}
	if len(byAction) != 0 {
		return fmt.Errorf("approval requests contain actions absent from checkpoint policies")
	}
	return nil
}
