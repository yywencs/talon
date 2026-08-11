package controller

import (
	"context"
	"fmt"
	"strings"

	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/workflow"
)

// ApprovalRequest 将人工身份和决定绑定到明确的 Plan ID。
type ApprovalRequest struct {
	PlanID   string `json:"plan_id"`
	Approver string `json:"approver"`
	Reason   string `json:"reason,omitempty"`
}

// EvaluatePolicy 在 Dry Run 成功后执行关闭式风险判断。
// 只有显式 low 且无需审批的能力自动通过，其他可用能力默认等待人工审批。
func (p *PlanProcessor) EvaluatePolicy(ctx context.Context) (workflow.PlanPolicyDecision, error) {
	if p == nil || p.platform == nil || p.workflow == nil {
		return workflow.PlanPolicyDecision{}, fmt.Errorf("plan processor is not initialized")
	}
	if err := ctx.Err(); err != nil {
		return workflow.PlanPolicyDecision{}, err
	}
	snapshot := p.workflow.Snapshot()
	if snapshot.PlanPolicy != nil {
		return *snapshot.PlanPolicy, nil
	}
	if snapshot.State != workflow.StatePlanned {
		return workflow.PlanPolicyDecision{}, fmt.Errorf("%w: plan policy is not allowed in state %q", workflow.ErrInvalidTransition, snapshot.State)
	}
	if snapshot.Plan == nil {
		return workflow.PlanPolicyDecision{}, fmt.Errorf("planned workflow has no frozen plan")
	}
	if snapshot.PlanDryRun == nil || snapshot.PlanDryRun.PlanID != snapshot.Plan.ID || snapshot.PlanDryRun.Status != workflow.PlanDryRunSucceeded {
		return workflow.PlanPolicyDecision{}, fmt.Errorf("plan policy requires a successful dry run for the current plan")
	}
	if strings.TrimSpace(snapshot.PlanDryRun.OperationID) == "" {
		return workflow.PlanPolicyDecision{}, fmt.Errorf("successful plan dry run has no operation ID")
	}

	capabilities, err := p.platform.GetRemediationCapabilities(ctx, platform.StateQuery{
		Scope: platform.Scope{IncidentID: snapshot.IncidentID},
	})
	if err != nil {
		return workflow.PlanPolicyDecision{}, fmt.Errorf("get remediation capabilities for policy: %w", err)
	}
	decision := evaluateCapabilityPolicy(*snapshot.Plan, snapshot.PlanDryRun.OperationID, capabilities)
	recorded, err := p.workflow.RecordPlanPolicyDecision(decision)
	if err != nil {
		return workflow.PlanPolicyDecision{}, fmt.Errorf("record plan policy decision: %w", err)
	}
	return recorded, nil
}

// Approve 由人工批准 awaiting_approval 中的同一份冻结 Plan。
func (p *PlanProcessor) Approve(ctx context.Context, request ApprovalRequest) (workflow.PlanApproval, error) {
	return p.decideApproval(ctx, request, workflow.PlanApprovalApproved)
}

// Reject 由人工拒绝 awaiting_approval 中的同一份冻结 Plan，拒绝原因必填。
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
		PlanID: request.PlanID, Decision: decision, Approver: request.Approver, Reason: request.Reason,
	})
	if err != nil {
		return workflow.PlanApproval{}, fmt.Errorf("record plan approval: %w", err)
	}
	return result, nil
}

func evaluateCapabilityPolicy(plan workflow.Plan, dryRunOperationID string, capabilities []platform.RemediationCapability) workflow.PlanPolicyDecision {
	decision := workflow.PlanPolicyDecision{
		PlanID: plan.ID, DryRunOperationID: dryRunOperationID, Risk: "unknown",
		Outcome: workflow.PlanPolicyRejected, ReasonCode: "capability_not_available",
		Reason: "planned remediation capability is no longer available",
	}
	var capability *platform.RemediationCapability
	for index := range capabilities {
		if strings.TrimSpace(capabilities[index].Name) == plan.Remediation.ToolName {
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
