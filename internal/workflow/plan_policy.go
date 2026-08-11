package workflow

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// PlanPolicyOutcome 表示 Policy 对一个冻结 Action 的判断。
type PlanPolicyOutcome string

const (
	PlanPolicyAutoApproved     PlanPolicyOutcome = "auto_approved"
	PlanPolicyApprovalRequired PlanPolicyOutcome = "approval_required"
	PlanPolicyRejected         PlanPolicyOutcome = "rejected"
)

// PlanPolicyDecision 将风险判断绑定到一个不可变的 Plan Action 及其 Dry Run。
type PlanPolicyDecision struct {
	PlanID            string            `json:"plan_id"`
	ActionID          string            `json:"action_id"`
	ActionDigest      string            `json:"action_digest"`
	DryRunOperationID string            `json:"dry_run_operation_id"`
	Risk              string            `json:"risk"`
	Outcome           PlanPolicyOutcome `json:"outcome"`
	ReasonCode        string            `json:"reason_code"`
	Reason            string            `json:"reason,omitempty"`
	EvaluatedAt       time.Time         `json:"evaluated_at"`
}

// PlanApprovalDecision 表示人工对一个 Plan Action 的决定。
type PlanApprovalDecision string

const (
	PlanApprovalApproved PlanApprovalDecision = "approved"
	PlanApprovalRejected PlanApprovalDecision = "rejected"
)

// PlanApproval 保存与具体 Action 绑定的不可变人工决定。
type PlanApproval struct {
	PlanID       string               `json:"plan_id"`
	ActionID     string               `json:"action_id"`
	ActionDigest string               `json:"action_digest"`
	Decision     PlanApprovalDecision `json:"decision"`
	Approver     string               `json:"approver"`
	Reason       string               `json:"reason,omitempty"`
	DecidedAt    time.Time            `json:"decided_at"`
}

// RecordPlanPolicyDecisions 原子保存当前 Plan 所有 Action 的 Policy 结果。
// 全部自动通过才进入 remediating；存在待审批 Action 时进入 awaiting_approval；
// 任一 Action 被 Policy 拒绝时，整份 Plan 返回 reinvestigating。
func (w *IncidentWorkflow) RecordPlanPolicyDecisions(decisions []PlanPolicyDecision) ([]PlanPolicyDecision, error) {
	if w == nil {
		return nil, fmt.Errorf("workflow is not initialized")
	}
	decisions = clonePlanPolicyDecisions(decisions)
	for index := range decisions {
		normalizePlanPolicyDecision(&decisions[index])
		if err := validatePlanPolicyDecision(decisions[index]); err != nil {
			return nil, fmt.Errorf("plan policy decision %d: %w", index, err)
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.planPolicies) > 0 {
		if !samePlanPolicyDecisions(w.planPolicies, decisions) {
			return nil, fmt.Errorf("plan actions already have immutable policy decisions")
		}
		return clonePlanPolicyDecisions(w.planPolicies), nil
	}
	if w.state != StatePlanned {
		return nil, fmt.Errorf("%w: plan policy is not allowed in state %q", ErrInvalidTransition, w.state)
	}
	if w.plan == nil || len(decisions) != len(w.plan.Actions) {
		return nil, fmt.Errorf("plan policy must contain exactly one decision for every frozen action")
	}

	byAction := make(map[string]PlanPolicyDecision, len(decisions))
	for _, decision := range decisions {
		if decision.PlanID != w.plan.ID {
			return nil, fmt.Errorf("plan policy does not match the current frozen plan")
		}
		if _, duplicate := byAction[decision.ActionID]; duplicate {
			return nil, fmt.Errorf("duplicate plan policy action_id %q", decision.ActionID)
		}
		byAction[decision.ActionID] = decision
	}
	for _, action := range w.plan.Actions {
		decision, ok := byAction[action.ID]
		if !ok || decision.ActionDigest != action.Digest {
			return nil, fmt.Errorf("plan policy does not match frozen action %q", action.ID)
		}
		dryRun := findPlanDryRun(w.planDryRuns, action.ID)
		if dryRun == nil || dryRun.PlanID != w.plan.ID || dryRun.ActionDigest != action.Digest || dryRun.Status != PlanDryRunSucceeded {
			return nil, fmt.Errorf("plan policy requires a successful dry run for action %q", action.ID)
		}
		if dryRun.OperationID != decision.DryRunOperationID {
			return nil, fmt.Errorf("plan policy dry run operation does not match action %q", action.ID)
		}
	}

	evaluatedAt := w.now()
	for index := range decisions {
		decisions[index].EvaluatedAt = evaluatedAt
	}
	w.planPolicies = clonePlanPolicyDecisions(decisions)

	outcome := PlanPolicyAutoApproved
	for _, decision := range decisions {
		if decision.Outcome == PlanPolicyRejected {
			outcome = PlanPolicyRejected
			break
		}
		if decision.Outcome == PlanPolicyApprovalRequired {
			outcome = PlanPolicyApprovalRequired
		}
	}
	metadata := map[string]string{
		"plan_id": w.plan.ID, "action_count": strconv.Itoa(len(decisions)), "policy_outcome": string(outcome),
	}
	event := Event{Actor: ActorWorkflow, Metadata: metadata}
	switch outcome {
	case PlanPolicyAutoApproved:
		event.Type = EventPlanApproved
		event.Reason = "all plan actions were auto-approved"
	case PlanPolicyApprovalRequired:
		event.Type = EventApprovalRequired
		event.Reason = "one or more plan actions require human approval"
	case PlanPolicyRejected:
		event.Type = EventPlanRejected
		event.Reason = "one or more plan actions were rejected by policy"
	}
	if _, err := w.applyLocked(event); err != nil {
		return nil, err
	}
	return clonePlanPolicyDecisions(w.planPolicies), nil
}

// RecordPlanApproval 保存一个 Action 的人工审批。只有全部待审批 Action 都通过后，
// Workflow 才会从 awaiting_approval 进入 remediating。
func (w *IncidentWorkflow) RecordPlanApproval(approval PlanApproval) (PlanApproval, error) {
	if w == nil {
		return PlanApproval{}, fmt.Errorf("workflow is not initialized")
	}
	normalizePlanApproval(&approval)
	if err := validatePlanApproval(approval); err != nil {
		return PlanApproval{}, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if existing := findPlanApproval(w.planApprovals, approval.ActionID); existing != nil && existing.PlanID == approval.PlanID {
		if existing.ActionDigest != approval.ActionDigest || existing.Decision != approval.Decision || existing.Approver != approval.Approver || existing.Reason != approval.Reason {
			return PlanApproval{}, fmt.Errorf("plan action approval already has an immutable decision by %q", existing.Approver)
		}
		return *clonePlanApprovalPointer(existing), nil
	}
	if w.state != StateAwaitingApproval {
		return PlanApproval{}, fmt.Errorf("%w: plan approval is not allowed in state %q", ErrInvalidTransition, w.state)
	}
	if w.plan == nil || w.plan.ID != approval.PlanID {
		return PlanApproval{}, fmt.Errorf("plan approval does not match the current frozen plan")
	}
	action := findPlannedAction(w.plan.Actions, approval.ActionID)
	if action == nil || action.Digest != approval.ActionDigest {
		return PlanApproval{}, fmt.Errorf("plan approval does not match a frozen action")
	}
	policy := findPlanPolicyDecision(w.planPolicies, approval.ActionID)
	if policy == nil || policy.Outcome != PlanPolicyApprovalRequired {
		return PlanApproval{}, fmt.Errorf("plan action approval requires an approval_required policy decision")
	}

	approval.DecidedAt = w.now()
	w.planApprovals = append(w.planApprovals, *clonePlanApprovalPointer(&approval))
	metadata := map[string]string{
		"plan_id": approval.PlanID, "action_id": approval.ActionID, "action_digest": approval.ActionDigest,
		"approval_decision": string(approval.Decision), "approver": approval.Approver,
	}
	if approval.Decision == PlanApprovalRejected {
		if _, err := w.applyLocked(Event{Type: EventPlanRejected, Actor: ActorHuman, Reason: approval.Reason, Metadata: metadata}); err != nil {
			return PlanApproval{}, err
		}
	} else if allRequiredActionsApproved(w.planPolicies, w.planApprovals) {
		if _, err := w.applyLocked(Event{Type: EventPlanApproved, Actor: ActorHuman, Reason: "all required plan actions were approved", Metadata: metadata}); err != nil {
			return PlanApproval{}, err
		}
	}
	return *clonePlanApprovalPointer(findPlanApproval(w.planApprovals, approval.ActionID)), nil
}

func normalizePlanPolicyDecision(value *PlanPolicyDecision) {
	value.PlanID = strings.TrimSpace(value.PlanID)
	value.ActionID = strings.TrimSpace(value.ActionID)
	value.ActionDigest = strings.TrimSpace(value.ActionDigest)
	value.DryRunOperationID = strings.TrimSpace(value.DryRunOperationID)
	value.Risk = strings.ToLower(strings.TrimSpace(value.Risk))
	value.ReasonCode = strings.TrimSpace(value.ReasonCode)
	value.Reason = strings.TrimSpace(value.Reason)
}

func validatePlanPolicyDecision(value PlanPolicyDecision) error {
	for field, content := range map[string]string{
		"plan_id": value.PlanID, "action_id": value.ActionID, "action_digest": value.ActionDigest,
		"dry_run_operation_id": value.DryRunOperationID, "risk": value.Risk, "reason_code": value.ReasonCode,
	} {
		if strings.TrimSpace(content) == "" {
			return fmt.Errorf("plan policy %s is required", field)
		}
	}
	switch value.Outcome {
	case PlanPolicyAutoApproved, PlanPolicyApprovalRequired, PlanPolicyRejected:
		return nil
	default:
		return fmt.Errorf("unknown plan policy outcome %q", value.Outcome)
	}
}

func normalizePlanApproval(value *PlanApproval) {
	value.PlanID = strings.TrimSpace(value.PlanID)
	value.ActionID = strings.TrimSpace(value.ActionID)
	value.ActionDigest = strings.TrimSpace(value.ActionDigest)
	value.Approver = strings.TrimSpace(value.Approver)
	value.Reason = strings.TrimSpace(value.Reason)
}

func validatePlanApproval(value PlanApproval) error {
	for field, content := range map[string]string{
		"plan_id": value.PlanID, "action_id": value.ActionID, "action_digest": value.ActionDigest, "approver": value.Approver,
	} {
		if strings.TrimSpace(content) == "" {
			return fmt.Errorf("plan approval %s is required", field)
		}
	}
	switch value.Decision {
	case PlanApprovalApproved:
		return nil
	case PlanApprovalRejected:
		if value.Reason == "" {
			return fmt.Errorf("rejected plan action approval reason is required")
		}
		return nil
	default:
		return fmt.Errorf("unknown plan approval decision %q", value.Decision)
	}
}

func findPlanPolicyDecision(values []PlanPolicyDecision, actionID string) *PlanPolicyDecision {
	for index := range values {
		if values[index].ActionID == actionID {
			return &values[index]
		}
	}
	return nil
}

func findPlanApproval(values []PlanApproval, actionID string) *PlanApproval {
	for index := range values {
		if values[index].ActionID == actionID {
			return &values[index]
		}
	}
	return nil
}

func allRequiredActionsApproved(policies []PlanPolicyDecision, approvals []PlanApproval) bool {
	for _, policy := range policies {
		if policy.Outcome != PlanPolicyApprovalRequired {
			continue
		}
		approval := findPlanApproval(approvals, policy.ActionID)
		if approval == nil || approval.Decision != PlanApprovalApproved || approval.ActionDigest != policy.ActionDigest {
			return false
		}
	}
	return true
}

func samePlanPolicyDecisions(left, right []PlanPolicyDecision) bool {
	if len(left) != len(right) {
		return false
	}
	for _, expected := range left {
		actual := findPlanPolicyDecision(right, expected.ActionID)
		if actual == nil || expected.PlanID != actual.PlanID || expected.ActionDigest != actual.ActionDigest ||
			expected.DryRunOperationID != actual.DryRunOperationID || expected.Risk != actual.Risk ||
			expected.Outcome != actual.Outcome || expected.ReasonCode != actual.ReasonCode || expected.Reason != actual.Reason {
			return false
		}
	}
	return true
}

func clonePlanPolicyDecisions(values []PlanPolicyDecision) []PlanPolicyDecision {
	return append([]PlanPolicyDecision(nil), values...)
}

func clonePlanApprovals(values []PlanApproval) []PlanApproval {
	return append([]PlanApproval(nil), values...)
}

func clonePlanApprovalPointer(value *PlanApproval) *PlanApproval {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
