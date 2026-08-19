package controller

import (
	"context"
	"fmt"
	"strings"

	"github.com/wen/opentalon/internal/approval"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/workflow"
)

// ApprovalRequest 将人工身份和决定绑定到明确的冻结 Action。
type ApprovalRequest struct {
	IntentID     string `json:"intent_id"`
	ActionID     string `json:"action_id"`
	ActionDigest string `json:"action_digest"`
	Approver     string `json:"approver"`
	Reason       string `json:"reason,omitempty"`
}

// EvaluatePolicy 在全部 Action Dry Run 成功后逐个执行关闭式风险判断。
// 只有显式 low 且无需审批的 Action 自动通过，其他可用 Action 默认等待人工审批。
func (p *ExecutionCoordinator) EvaluatePolicy(ctx context.Context) ([]workflow.ActionPolicyDecision, error) {
	if p == nil || p.platform == nil || p.workflow == nil {
		return nil, fmt.Errorf("execution coordinator is not initialized")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snapshot := p.workflow.Snapshot()
	if len(snapshot.ActionPolicies) > 0 {
		if requiresHumanApproval(snapshot.ActionPolicies) && p.approvalStore == nil {
			return snapshot.ActionPolicies, fmt.Errorf("approval store is required for actions awaiting human approval")
		}
		if err := p.ensureApprovalRequests(ctx, snapshot); err != nil {
			return snapshot.ActionPolicies, err
		}
		return snapshot.ActionPolicies, nil
	}
	if snapshot.State != workflow.StateValidating {
		return nil, fmt.Errorf("%w: intent policy is not allowed in state %q", workflow.ErrInvalidTransition, snapshot.State)
	}
	if snapshot.ExecutionIntent == nil {
		return nil, fmt.Errorf("validating workflow has no frozen intent")
	}
	actions := workflow.ExecutableActions(snapshot)
	if len(actions) == 0 || len(snapshot.ActionDryRuns) != len(actions) {
		return nil, fmt.Errorf("intent policy requires a successful dry run for every action")
	}

	capabilities, err := p.platform.GetRemediationCapabilities(ctx, platform.StateQuery{
		Scope: platform.Scope{IncidentID: snapshot.IncidentID},
	})
	if err != nil {
		return nil, fmt.Errorf("get remediation capabilities for policy: %w", err)
	}
	decisions := make([]workflow.ActionPolicyDecision, 0, len(actions))
	for _, action := range actions {
		dryRun := actionDryRun(snapshot.ActionDryRuns, action.ID)
		if dryRun == nil || dryRun.IntentID != snapshot.ExecutionIntent.ID || dryRun.ActionDigest != action.Digest ||
			dryRun.Status != workflow.ActionDryRunSucceeded || strings.TrimSpace(dryRun.OperationID) == "" {
			return nil, fmt.Errorf("intent policy requires a successful dry run with operation ID for action %q", action.ID)
		}
		decisions = append(decisions, evaluateCapabilityPolicy(snapshot.ExecutionIntent.ID, action, dryRun.OperationID, capabilities))
	}
	if requiresHumanApproval(decisions) && p.approvalStore == nil {
		return decisions, fmt.Errorf("approval store is required for actions awaiting human approval")
	}
	recorded, err := p.workflow.RecordActionPolicyDecisions(decisions)
	if err != nil {
		return nil, fmt.Errorf("record intent policy decisions: %w", err)
	}
	if err := p.ensureApprovalRequests(ctx, p.workflow.Snapshot()); err != nil {
		return recorded, err
	}
	return recorded, nil
}

// Approve 由人工批准 awaiting_approval 中的一个冻结 Action。
func (p *ExecutionCoordinator) Approve(ctx context.Context, request ApprovalRequest) (workflow.ActionApproval, error) {
	return p.decideApproval(ctx, request, workflow.ActionApprovalApproved)
}

// Reject 由人工拒绝 awaiting_approval 中的一个冻结 Action，拒绝原因必填。
func (p *ExecutionCoordinator) Reject(ctx context.Context, request ApprovalRequest) (workflow.ActionApproval, error) {
	return p.decideApproval(ctx, request, workflow.ActionApprovalRejected)
}

func (p *ExecutionCoordinator) decideApproval(ctx context.Context, request ApprovalRequest, decision workflow.ActionApprovalDecision) (workflow.ActionApproval, error) {
	if p == nil || p.workflow == nil {
		return workflow.ActionApproval{}, fmt.Errorf("execution coordinator is not initialized")
	}
	if err := ctx.Err(); err != nil {
		return workflow.ActionApproval{}, err
	}
	if p.approvalStore == nil {
		return workflow.ActionApproval{}, fmt.Errorf("approval store is required for human decisions")
	}
	status := approval.StatusApproved
	if decision == workflow.ActionApprovalRejected {
		status = approval.StatusRejected
	}
	if _, err := p.approvalStore.Decide(ctx, approval.Decision{
		ID: approval.RequestID(request.ActionID), IntentID: request.IntentID, ActionID: request.ActionID,
		ActionDigest: request.ActionDigest, Status: status, DecidedBy: request.Approver,
		DecisionReason: request.Reason,
	}); err != nil {
		return workflow.ActionApproval{}, fmt.Errorf("persist intent action approval: %w", err)
	}
	result, err := p.workflow.RecordActionApproval(workflow.ActionApproval{
		IntentID: request.IntentID, ActionID: request.ActionID, ActionDigest: request.ActionDigest,
		Decision: decision, Approver: request.Approver, Reason: request.Reason,
	})
	if err != nil {
		return workflow.ActionApproval{}, fmt.Errorf("record intent approval: %w", err)
	}
	if err := p.persistCheckpoint(ctx); err != nil {
		return result, fmt.Errorf("persist intent approval checkpoint: %w", err)
	}
	return result, nil
}

// ListPendingApprovals 返回当前持久化审批收件箱中的待处理 Action。
func (p *ExecutionCoordinator) ListPendingApprovals(ctx context.Context) ([]approval.Request, error) {
	if p == nil || p.approvalStore == nil {
		return nil, fmt.Errorf("approval store is not configured")
	}
	return p.approvalStore.ListPending(ctx)
}

func (p *ExecutionCoordinator) ensureApprovalRequests(ctx context.Context, snapshot workflow.Snapshot) error {
	if p.approvalStore == nil {
		return nil
	}
	if snapshot.ExecutionIntent == nil {
		return fmt.Errorf("persist approval requests: workflow has no frozen intent")
	}
	for _, policy := range snapshot.ActionPolicies {
		if policy.Outcome != workflow.ActionPolicyApprovalRequired {
			continue
		}
		action := intendedAction(workflow.ExecutableActions(snapshot), policy.ActionID)
		if action == nil || action.Digest != policy.ActionDigest {
			return fmt.Errorf("persist approval request: policy does not match frozen action %q", policy.ActionID)
		}
		_, err := p.approvalStore.Create(ctx, approval.Request{
			ID: approval.RequestID(action.ID), IncidentID: snapshot.IncidentID,
			IntentID: snapshot.ExecutionIntent.ID, ActionID: action.ID, ActionDigest: action.Digest,
			DryRunOperationID: policy.DryRunOperationID, ToolName: action.ToolName,
			Arguments: cloneMap(action.Arguments), Risk: policy.Risk, PolicyReason: policy.Reason,
		})
		if err != nil {
			return fmt.Errorf("persist approval request for action %q: %w", action.ID, err)
		}
	}
	return nil
}

func intendedAction(values []workflow.IntendedAction, actionID string) *workflow.IntendedAction {
	for index := range values {
		if values[index].ID == actionID {
			return &values[index]
		}
	}
	return nil
}

func requiresHumanApproval(decisions []workflow.ActionPolicyDecision) bool {
	for _, decision := range decisions {
		if decision.Outcome == workflow.ActionPolicyApprovalRequired {
			return true
		}
	}
	return false
}

func evaluateCapabilityPolicy(intentID string, action workflow.IntendedAction, dryRunOperationID string, capabilities []platform.RemediationCapability) workflow.ActionPolicyDecision {
	decision := workflow.ActionPolicyDecision{
		IntentID: intentID, ActionID: action.ID, ActionDigest: action.Digest,
		DryRunOperationID: dryRunOperationID, Risk: "unknown",
		Outcome: workflow.ActionPolicyRejected, ReasonCode: "capability_not_available",
		Reason: "remediation capability is no longer available",
	}
	if action.Kind == workflow.ActionKindProbe || action.Kind == workflow.ActionKindRecovery {
		decision.Risk = "low"
		decision.Outcome = workflow.ActionPolicyAutoApproved
		decision.ReasonCode = "harness_managed_action"
		decision.Reason = "probe and recovery actions are constrained by a validated harness recovery policy"
		return decision
	}
	var capability *platform.RemediationCapability
	for index := range capabilities {
		if strings.TrimSpace(capabilities[index].Name) == action.ToolName {
			capability = &capabilities[index]
			break
		}
	}
	if capability == nil {
		return decision
	}
	risk := strings.ToLower(strings.TrimSpace(capability.Risk))
	if risk == "" {
		risk = "unknown"
	}
	decision.Risk = risk
	if capability.RequiresApproval {
		decision.Outcome = workflow.ActionPolicyApprovalRequired
		decision.ReasonCode = "capability_requires_approval"
		decision.Reason = "remediation capability explicitly requires human approval"
		return decision
	}
	if risk == "low" {
		decision.Outcome = workflow.ActionPolicyAutoApproved
		decision.ReasonCode = "low_risk_auto_approved"
		decision.Reason = "low-risk remediation is explicitly allowed without approval"
		return decision
	}
	decision.Outcome = workflow.ActionPolicyApprovalRequired
	decision.ReasonCode = "non_low_risk_requires_approval"
	decision.Reason = "non-low or unknown risk requires human approval by default"
	return decision
}

func actionDryRun(values []workflow.ActionDryRun, actionID string) *workflow.ActionDryRun {
	for index := range values {
		if values[index].ActionID == actionID {
			return &values[index]
		}
	}
	return nil
}
