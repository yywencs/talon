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
	PlanID       string `json:"plan_id"`
	ActionID     string `json:"action_id"`
	ActionDigest string `json:"action_digest"`
	Approver     string `json:"approver"`
	Reason       string `json:"reason,omitempty"`
}

// EvaluatePolicy 在全部 Action Dry Run 成功后逐个执行关闭式风险判断。
// 只有显式 low 且无需审批的 Action 自动通过，其他可用 Action 默认等待人工审批。
func (p *PlanProcessor) EvaluatePolicy(ctx context.Context) ([]workflow.PlanPolicyDecision, error) {
	if p == nil || p.platform == nil || p.workflow == nil {
		return nil, fmt.Errorf("plan processor is not initialized")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snapshot := p.workflow.Snapshot()
	if len(snapshot.PlanPolicies) > 0 {
		if requiresHumanApproval(snapshot.PlanPolicies) && p.approvalStore == nil {
			return snapshot.PlanPolicies, fmt.Errorf("approval store is required for actions awaiting human approval")
		}
		if err := p.ensureApprovalRequests(ctx, snapshot); err != nil {
			return snapshot.PlanPolicies, err
		}
		return snapshot.PlanPolicies, nil
	}
	if snapshot.State != workflow.StatePlanned {
		return nil, fmt.Errorf("%w: plan policy is not allowed in state %q", workflow.ErrInvalidTransition, snapshot.State)
	}
	if snapshot.Plan == nil {
		return nil, fmt.Errorf("planned workflow has no frozen plan")
	}
	if len(snapshot.PlanDryRuns) != len(snapshot.Plan.Actions) {
		return nil, fmt.Errorf("plan policy requires a successful dry run for every action")
	}

	capabilities, err := p.platform.GetRemediationCapabilities(ctx, platform.StateQuery{
		Scope: platform.Scope{IncidentID: snapshot.IncidentID},
	})
	if err != nil {
		return nil, fmt.Errorf("get remediation capabilities for policy: %w", err)
	}
	decisions := make([]workflow.PlanPolicyDecision, 0, len(snapshot.Plan.Actions))
	for _, action := range snapshot.Plan.Actions {
		dryRun := actionDryRun(snapshot.PlanDryRuns, action.ID)
		if dryRun == nil || dryRun.PlanID != snapshot.Plan.ID || dryRun.ActionDigest != action.Digest ||
			dryRun.Status != workflow.PlanDryRunSucceeded || strings.TrimSpace(dryRun.OperationID) == "" {
			return nil, fmt.Errorf("plan policy requires a successful dry run with operation ID for action %q", action.ID)
		}
		decisions = append(decisions, evaluateCapabilityPolicy(snapshot.Plan.ID, action, dryRun.OperationID, capabilities))
	}
	if requiresHumanApproval(decisions) && p.approvalStore == nil {
		return decisions, fmt.Errorf("approval store is required for actions awaiting human approval")
	}
	recorded, err := p.workflow.RecordPlanPolicyDecisions(decisions)
	if err != nil {
		return nil, fmt.Errorf("record plan policy decisions: %w", err)
	}
	if err := p.ensureApprovalRequests(ctx, p.workflow.Snapshot()); err != nil {
		return recorded, err
	}
	return recorded, nil
}

// Approve 由人工批准 awaiting_approval 中的一个冻结 Action。
func (p *PlanProcessor) Approve(ctx context.Context, request ApprovalRequest) (workflow.PlanApproval, error) {
	return p.decideApproval(ctx, request, workflow.PlanApprovalApproved)
}

// Reject 由人工拒绝 awaiting_approval 中的一个冻结 Action，拒绝原因必填。
func (p *PlanProcessor) Reject(ctx context.Context, request ApprovalRequest) (workflow.PlanApproval, error) {
	return p.decideApproval(ctx, request, workflow.PlanApprovalRejected)
}

func (p *PlanProcessor) decideApproval(ctx context.Context, request ApprovalRequest, decision workflow.PlanApprovalDecision) (workflow.PlanApproval, error) {
	if p == nil || p.workflow == nil {
		return workflow.PlanApproval{}, fmt.Errorf("plan processor is not initialized")
	}
	if err := ctx.Err(); err != nil {
		return workflow.PlanApproval{}, err
	}
	if p.approvalStore == nil {
		return workflow.PlanApproval{}, fmt.Errorf("approval store is required for human decisions")
	}
	status := approval.StatusApproved
	if decision == workflow.PlanApprovalRejected {
		status = approval.StatusRejected
	}
	if _, err := p.approvalStore.Decide(ctx, approval.Decision{
		ID: approval.RequestID(request.ActionID), PlanID: request.PlanID, ActionID: request.ActionID,
		ActionDigest: request.ActionDigest, Status: status, DecidedBy: request.Approver,
		DecisionReason: request.Reason,
	}); err != nil {
		return workflow.PlanApproval{}, fmt.Errorf("persist plan action approval: %w", err)
	}
	result, err := p.workflow.RecordPlanApproval(workflow.PlanApproval{
		PlanID: request.PlanID, ActionID: request.ActionID, ActionDigest: request.ActionDigest,
		Decision: decision, Approver: request.Approver, Reason: request.Reason,
	})
	if err != nil {
		return workflow.PlanApproval{}, fmt.Errorf("record plan approval: %w", err)
	}
	return result, nil
}

// ListPendingApprovals 返回当前持久化审批收件箱中的待处理 Action。
func (p *PlanProcessor) ListPendingApprovals(ctx context.Context) ([]approval.Request, error) {
	if p == nil || p.approvalStore == nil {
		return nil, fmt.Errorf("approval store is not configured")
	}
	return p.approvalStore.ListPending(ctx)
}

func (p *PlanProcessor) ensureApprovalRequests(ctx context.Context, snapshot workflow.Snapshot) error {
	if p.approvalStore == nil {
		return nil
	}
	if snapshot.Plan == nil {
		return fmt.Errorf("persist approval requests: workflow has no frozen plan")
	}
	for _, policy := range snapshot.PlanPolicies {
		if policy.Outcome != workflow.PlanPolicyApprovalRequired {
			continue
		}
		action := plannedAction(snapshot.Plan.Actions, policy.ActionID)
		if action == nil || action.Digest != policy.ActionDigest {
			return fmt.Errorf("persist approval request: policy does not match frozen action %q", policy.ActionID)
		}
		_, err := p.approvalStore.Create(ctx, approval.Request{
			ID: approval.RequestID(action.ID), IncidentID: snapshot.IncidentID,
			PlanID: snapshot.Plan.ID, ActionID: action.ID, ActionDigest: action.Digest,
			DryRunOperationID: policy.DryRunOperationID, ToolName: action.ToolName,
			Arguments: cloneMap(action.Arguments), Risk: policy.Risk, PolicyReason: policy.Reason,
		})
		if err != nil {
			return fmt.Errorf("persist approval request for action %q: %w", action.ID, err)
		}
	}
	return nil
}

func plannedAction(values []workflow.PlannedAction, actionID string) *workflow.PlannedAction {
	for index := range values {
		if values[index].ID == actionID {
			return &values[index]
		}
	}
	return nil
}

func requiresHumanApproval(decisions []workflow.PlanPolicyDecision) bool {
	for _, decision := range decisions {
		if decision.Outcome == workflow.PlanPolicyApprovalRequired {
			return true
		}
	}
	return false
}

func evaluateCapabilityPolicy(planID string, action workflow.PlannedAction, dryRunOperationID string, capabilities []platform.RemediationCapability) workflow.PlanPolicyDecision {
	decision := workflow.PlanPolicyDecision{
		PlanID: planID, ActionID: action.ID, ActionDigest: action.Digest,
		DryRunOperationID: dryRunOperationID, Risk: "unknown",
		Outcome: workflow.PlanPolicyRejected, ReasonCode: "capability_not_available",
		Reason: "planned remediation capability is no longer available",
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
		decision.Outcome = workflow.PlanPolicyApprovalRequired
		decision.ReasonCode = "capability_requires_approval"
		decision.Reason = "remediation capability explicitly requires human approval"
		return decision
	}
	if risk == "low" {
		decision.Outcome = workflow.PlanPolicyAutoApproved
		decision.ReasonCode = "low_risk_auto_approved"
		decision.Reason = "low-risk remediation is explicitly allowed without approval"
		return decision
	}
	decision.Outcome = workflow.PlanPolicyApprovalRequired
	decision.ReasonCode = "non_low_risk_requires_approval"
	decision.Reason = "non-low or unknown risk requires human approval by default"
	return decision
}

func actionDryRun(values []workflow.PlanDryRun, actionID string) *workflow.PlanDryRun {
	for index := range values {
		if values[index].ActionID == actionID {
			return &values[index]
		}
	}
	return nil
}
