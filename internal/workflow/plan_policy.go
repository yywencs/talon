package workflow

import (
	"fmt"
	"strings"
	"time"
)

// PlanPolicyOutcome 表示确定性 Policy 对冻结 Plan 的判断。
type PlanPolicyOutcome string

const (
	PlanPolicyAutoApproved     PlanPolicyOutcome = "auto_approved"
	PlanPolicyApprovalRequired PlanPolicyOutcome = "approval_required"
	PlanPolicyRejected         PlanPolicyOutcome = "rejected"
)

// PlanPolicyDecision 将风险、Dry Run 和审批要求绑定到同一个不可变 Plan。
type PlanPolicyDecision struct {
	PlanID            string            `json:"plan_id"`
	DryRunOperationID string            `json:"dry_run_operation_id"`
	Risk              string            `json:"risk"`
	Outcome           PlanPolicyOutcome `json:"outcome"`
	ReasonCode        string            `json:"reason_code"`
	Reason            string            `json:"reason,omitempty"`
	EvaluatedAt       time.Time         `json:"evaluated_at"`
}

// PlanApprovalDecision 表示人工对等待审批 Plan 的决定。
type PlanApprovalDecision string

const (
	PlanApprovalApproved PlanApprovalDecision = "approved"
	PlanApprovalRejected PlanApprovalDecision = "rejected"
)

// PlanApproval 保存审批人以及与 Plan ID 绑定的最终决定。
type PlanApproval struct {
	PlanID    string               `json:"plan_id"`
	Decision  PlanApprovalDecision `json:"decision"`
	Approver  string               `json:"approver"`
	Reason    string               `json:"reason,omitempty"`
	DecidedAt time.Time            `json:"decided_at"`
}

// RecordPlanPolicyDecision 保存 Policy 结果并推进到 remediating、awaiting_approval
// 或 reinvestigating。只有成功完成同一 Plan 的 Dry Run 才允许评估。
func (w *IncidentWorkflow) RecordPlanPolicyDecision(decision PlanPolicyDecision) (PlanPolicyDecision, error) {
	if w == nil {
		return PlanPolicyDecision{}, fmt.Errorf("workflow is not initialized")
	}
	decision.PlanID = strings.TrimSpace(decision.PlanID)
	decision.DryRunOperationID = strings.TrimSpace(decision.DryRunOperationID)
	decision.Risk = strings.ToLower(strings.TrimSpace(decision.Risk))
	decision.ReasonCode = strings.TrimSpace(decision.ReasonCode)
	decision.Reason = strings.TrimSpace(decision.Reason)
	if err := validatePlanPolicyDecision(decision); err != nil {
		return PlanPolicyDecision{}, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.planPolicy != nil && w.planPolicy.PlanID == decision.PlanID {
		if w.planPolicy.DryRunOperationID != decision.DryRunOperationID || w.planPolicy.Risk != decision.Risk ||
			w.planPolicy.Outcome != decision.Outcome || w.planPolicy.ReasonCode != decision.ReasonCode || w.planPolicy.Reason != decision.Reason {
			return PlanPolicyDecision{}, fmt.Errorf("plan policy already has an immutable decision %q", w.planPolicy.Outcome)
		}
		return *clonePlanPolicyDecisionPointer(w.planPolicy), nil
	}
	if w.state != StatePlanned {
		return PlanPolicyDecision{}, fmt.Errorf("%w: plan policy is not allowed in state %q", ErrInvalidTransition, w.state)
	}
	if w.plan == nil || w.plan.ID != decision.PlanID {
		return PlanPolicyDecision{}, fmt.Errorf("plan policy does not match the current frozen plan")
	}
	if w.planDryRun == nil || w.planDryRun.PlanID != decision.PlanID || w.planDryRun.Status != PlanDryRunSucceeded {
		return PlanPolicyDecision{}, fmt.Errorf("plan policy requires a successful dry run for the same plan")
	}
	if w.planDryRun.OperationID != decision.DryRunOperationID {
		return PlanPolicyDecision{}, fmt.Errorf("plan policy dry run operation does not match the frozen result")
	}

	decision.EvaluatedAt = w.now()
	w.planPolicy = clonePlanPolicyDecisionPointer(&decision)
	metadata := map[string]string{
		"plan_id": decision.PlanID, "dry_run_operation_id": decision.DryRunOperationID,
		"risk": decision.Risk, "policy_outcome": string(decision.Outcome), "policy_reason_code": decision.ReasonCode,
	}
	event := Event{Actor: ActorWorkflow, Reason: decision.Reason, Metadata: metadata}
	switch decision.Outcome {
	case PlanPolicyAutoApproved:
		event.Type = EventPlanApproved
	case PlanPolicyApprovalRequired:
		event.Type = EventApprovalRequired
	case PlanPolicyRejected:
		event.Type = EventPlanRejected
	}
	if _, err := w.applyLocked(event); err != nil {
		return PlanPolicyDecision{}, err
	}
	return *clonePlanPolicyDecisionPointer(w.planPolicy), nil
}

// RecordPlanApproval 保存人工审批，并只推进与当前 Policy 绑定的 Plan。
func (w *IncidentWorkflow) RecordPlanApproval(approval PlanApproval) (PlanApproval, error) {
	if w == nil {
		return PlanApproval{}, fmt.Errorf("workflow is not initialized")
	}
	approval.PlanID = strings.TrimSpace(approval.PlanID)
	approval.Approver = strings.TrimSpace(approval.Approver)
	approval.Reason = strings.TrimSpace(approval.Reason)
	if err := validatePlanApproval(approval); err != nil {
		return PlanApproval{}, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.planApproval != nil && w.planApproval.PlanID == approval.PlanID {
		if w.planApproval.Decision != approval.Decision || w.planApproval.Approver != approval.Approver || w.planApproval.Reason != approval.Reason {
			return PlanApproval{}, fmt.Errorf("plan approval already has an immutable decision by %q", w.planApproval.Approver)
		}
		return *clonePlanApprovalPointer(w.planApproval), nil
	}
	if w.state != StateAwaitingApproval {
		return PlanApproval{}, fmt.Errorf("%w: plan approval is not allowed in state %q", ErrInvalidTransition, w.state)
	}
	if w.plan == nil || w.plan.ID != approval.PlanID {
		return PlanApproval{}, fmt.Errorf("plan approval does not match the current frozen plan")
	}
	if w.planPolicy == nil || w.planPolicy.PlanID != approval.PlanID || w.planPolicy.Outcome != PlanPolicyApprovalRequired {
		return PlanApproval{}, fmt.Errorf("plan approval requires an approval_required policy decision")
	}

	approval.DecidedAt = w.now()
	w.planApproval = clonePlanApprovalPointer(&approval)
	metadata := map[string]string{
		"plan_id": approval.PlanID, "approval_decision": string(approval.Decision), "approver": approval.Approver,
	}
	event := Event{Actor: ActorHuman, Reason: approval.Reason, Metadata: metadata}
	if approval.Decision == PlanApprovalApproved {
		event.Type = EventPlanApproved
	} else {
		event.Type = EventPlanRejected
	}
	if _, err := w.applyLocked(event); err != nil {
		return PlanApproval{}, err
	}
	return *clonePlanApprovalPointer(w.planApproval), nil
}

func validatePlanPolicyDecision(value PlanPolicyDecision) error {
	if strings.TrimSpace(value.PlanID) == "" {
		return fmt.Errorf("plan policy plan_id is required")
	}
	if strings.TrimSpace(value.DryRunOperationID) == "" {
		return fmt.Errorf("plan policy dry_run_operation_id is required")
	}
	if strings.TrimSpace(value.Risk) == "" {
		return fmt.Errorf("plan policy risk is required")
	}
	if strings.TrimSpace(value.ReasonCode) == "" {
		return fmt.Errorf("plan policy reason_code is required")
	}
	switch value.Outcome {
	case PlanPolicyAutoApproved, PlanPolicyApprovalRequired, PlanPolicyRejected:
		return nil
	default:
		return fmt.Errorf("unknown plan policy outcome %q", value.Outcome)
	}
}

func validatePlanApproval(value PlanApproval) error {
	if strings.TrimSpace(value.PlanID) == "" {
		return fmt.Errorf("plan approval plan_id is required")
	}
	if strings.TrimSpace(value.Approver) == "" {
		return fmt.Errorf("plan approval approver is required")
	}
	switch value.Decision {
	case PlanApprovalApproved:
		return nil
	case PlanApprovalRejected:
		if strings.TrimSpace(value.Reason) == "" {
			return fmt.Errorf("rejected plan approval reason is required")
		}
		return nil
	default:
		return fmt.Errorf("unknown plan approval decision %q", value.Decision)
	}
}

func clonePlanPolicyDecisionPointer(value *PlanPolicyDecision) *PlanPolicyDecision {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func clonePlanApprovalPointer(value *PlanApproval) *PlanApproval {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
