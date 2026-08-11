package controller

import (
	"context"
	"fmt"
	"strings"

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
	recorded, err := p.workflow.RecordPlanPolicyDecisions(decisions)
	if err != nil {
		return nil, fmt.Errorf("record plan policy decisions: %w", err)
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
	result, err := p.workflow.RecordPlanApproval(workflow.PlanApproval{
		PlanID: request.PlanID, ActionID: request.ActionID, ActionDigest: request.ActionDigest,
		Decision: decision, Approver: request.Approver, Reason: request.Reason,
	})
	if err != nil {
		return workflow.PlanApproval{}, fmt.Errorf("record plan approval: %w", err)
	}
	return result, nil
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
