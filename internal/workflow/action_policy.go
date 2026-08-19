package workflow

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ActionPolicyOutcome 表示 Policy 对一个冻结 Action 的判断。
type ActionPolicyOutcome string

const (
	ActionPolicyAutoApproved     ActionPolicyOutcome = "auto_approved"
	ActionPolicyApprovalRequired ActionPolicyOutcome = "approval_required"
	ActionPolicyRejected         ActionPolicyOutcome = "rejected"
)

// ActionPolicyDecision 将风险判断绑定到一个不可变的 ExecutionIntent Action 及其 Dry Run。
type ActionPolicyDecision struct {
	IntentID          string              `json:"intent_id"`
	ActionID          string              `json:"action_id"`
	ActionDigest      string              `json:"action_digest"`
	DryRunOperationID string              `json:"dry_run_operation_id"`
	Risk              string              `json:"risk"`
	Outcome           ActionPolicyOutcome `json:"outcome"`
	ReasonCode        string              `json:"reason_code"`
	Reason            string              `json:"reason,omitempty"`
	EvaluatedAt       time.Time           `json:"evaluated_at"`
}

// ActionApprovalDecision 表示人工对一个 ExecutionIntent Action 的决定。
type ActionApprovalDecision string

const (
	ActionApprovalApproved ActionApprovalDecision = "approved"
	ActionApprovalRejected ActionApprovalDecision = "rejected"
)

// ActionApproval 保存与具体 Action 绑定的不可变人工决定。
type ActionApproval struct {
	IntentID     string                 `json:"intent_id"`
	ActionID     string                 `json:"action_id"`
	ActionDigest string                 `json:"action_digest"`
	Decision     ActionApprovalDecision `json:"decision"`
	Approver     string                 `json:"approver"`
	Reason       string                 `json:"reason,omitempty"`
	DecidedAt    time.Time              `json:"decided_at"`
}

// RecordActionPolicyDecisions 原子保存当前 ExecutionIntent 所有 Action 的 Policy 结果。
// 全部自动通过才进入 executing；存在待审批 Action 时进入 awaiting_approval；
// 任一 Action 被 Policy 拒绝时，Workflow 返回 investigating 交给 Agent 决策。
func (w *IncidentWorkflow) RecordActionPolicyDecisions(decisions []ActionPolicyDecision) ([]ActionPolicyDecision, error) {
	if w == nil {
		return nil, fmt.Errorf("workflow is not initialized")
	}
	decisions = cloneActionPolicyDecisions(decisions)
	for index := range decisions {
		normalizeActionPolicyDecision(&decisions[index])
		if err := validateActionPolicyDecision(decisions[index]); err != nil {
			return nil, fmt.Errorf("intent policy decision %d: %w", index, err)
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.actionPolicies) > 0 {
		if !sameActionPolicyDecisions(w.actionPolicies, decisions) {
			return nil, fmt.Errorf("intent actions already have immutable policy decisions")
		}
		return cloneActionPolicyDecisions(w.actionPolicies), nil
	}
	if w.state != StateValidating {
		return nil, fmt.Errorf("%w: intent policy is not allowed in state %q", ErrInvalidTransition, w.state)
	}
	actions := w.executableActionsLocked()
	if w.intent == nil || len(decisions) != len(actions) {
		return nil, fmt.Errorf("intent policy must contain exactly one decision for every frozen action")
	}

	byAction := make(map[string]ActionPolicyDecision, len(decisions))
	for _, decision := range decisions {
		if decision.IntentID != w.intent.ID {
			return nil, fmt.Errorf("intent policy does not match the current frozen intent")
		}
		if _, duplicate := byAction[decision.ActionID]; duplicate {
			return nil, fmt.Errorf("duplicate intent policy action_id %q", decision.ActionID)
		}
		byAction[decision.ActionID] = decision
	}
	for _, action := range actions {
		decision, ok := byAction[action.ID]
		if !ok || decision.ActionDigest != action.Digest {
			return nil, fmt.Errorf("intent policy does not match frozen action %q", action.ID)
		}
		dryRun := findActionDryRun(w.actionDryRuns, action.ID)
		if dryRun == nil || dryRun.IntentID != w.intent.ID || dryRun.ActionDigest != action.Digest || dryRun.Status != ActionDryRunSucceeded {
			return nil, fmt.Errorf("intent policy requires a successful dry run for action %q", action.ID)
		}
		if dryRun.OperationID != decision.DryRunOperationID {
			return nil, fmt.Errorf("intent policy dry run operation does not match action %q", action.ID)
		}
	}

	evaluatedAt := w.now()
	for index := range decisions {
		decisions[index].EvaluatedAt = evaluatedAt
	}
	w.actionPolicies = cloneActionPolicyDecisions(decisions)
	w.allActionPolicies = append(w.allActionPolicies, cloneActionPolicyDecisions(decisions)...)

	outcome := ActionPolicyAutoApproved
	for _, decision := range decisions {
		if decision.Outcome == ActionPolicyRejected {
			outcome = ActionPolicyRejected
			break
		}
		if decision.Outcome == ActionPolicyApprovalRequired {
			outcome = ActionPolicyApprovalRequired
		}
	}
	metadata := map[string]string{
		"intent_id": w.intent.ID, "action_count": strconv.Itoa(len(decisions)), "policy_outcome": string(outcome),
	}
	event := Event{Actor: ActorWorkflow, Metadata: metadata}
	switch outcome {
	case ActionPolicyAutoApproved:
		event.Type = EventExecutionAuthorized
		event.Reason = "all intent actions were auto-approved"
	case ActionPolicyApprovalRequired:
		event.Type = EventApprovalRequired
		event.Reason = "one or more intent actions require human approval"
	case ActionPolicyRejected:
		event.Reason = "one or more intent actions were rejected by policy"
		if stage := w.currentStageLocked(); stage != nil {
			checkpoint := w.newCheckpointLocked(stage.StageID, "policy", CheckpointBlocked, event.Reason, "")
			w.checkpoints = append(w.checkpoints, checkpoint)
			event.Type = EventCheckpointBlocked
			event.Metadata["stage_id"] = stage.StageID
			event.Metadata["checkpoint_id"] = checkpoint.CheckpointID
		} else {
			event.Type = EventExecutionIntentRejected
		}
	}
	if _, err := w.applyLocked(event); err != nil {
		return nil, err
	}
	return cloneActionPolicyDecisions(w.actionPolicies), nil
}

// RecordActionApproval 保存一个 Action 的人工审批。只有全部待审批 Action 都通过后，
// Workflow 才会从 awaiting_approval 进入 executing。
func (w *IncidentWorkflow) RecordActionApproval(approval ActionApproval) (ActionApproval, error) {
	if w == nil {
		return ActionApproval{}, fmt.Errorf("workflow is not initialized")
	}
	normalizeActionApproval(&approval)
	if err := validateActionApproval(approval); err != nil {
		return ActionApproval{}, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if existing := findActionApproval(w.actionApprovals, approval.ActionID); existing != nil && existing.IntentID == approval.IntentID {
		if existing.ActionDigest != approval.ActionDigest || existing.Decision != approval.Decision || existing.Approver != approval.Approver || existing.Reason != approval.Reason {
			return ActionApproval{}, fmt.Errorf("intent action approval already has an immutable decision by %q", existing.Approver)
		}
		return *cloneActionApprovalPointer(existing), nil
	}
	if w.state != StateAwaitingApproval {
		return ActionApproval{}, fmt.Errorf("%w: intent approval is not allowed in state %q", ErrInvalidTransition, w.state)
	}
	if w.intent == nil || w.intent.ID != approval.IntentID {
		return ActionApproval{}, fmt.Errorf("intent approval does not match the current frozen intent")
	}
	action := findIntendedAction(w.executableActionsLocked(), approval.ActionID)
	if action == nil || action.Digest != approval.ActionDigest {
		return ActionApproval{}, fmt.Errorf("intent approval does not match a frozen action")
	}
	policy := findActionPolicyDecision(w.actionPolicies, approval.ActionID)
	if policy == nil || policy.Outcome != ActionPolicyApprovalRequired {
		return ActionApproval{}, fmt.Errorf("intent action approval requires an approval_required policy decision")
	}

	approval.DecidedAt = w.now()
	w.actionApprovals = append(w.actionApprovals, *cloneActionApprovalPointer(&approval))
	w.allActionApprovals = append(w.allActionApprovals, *cloneActionApprovalPointer(&approval))
	metadata := map[string]string{
		"intent_id": approval.IntentID, "action_id": approval.ActionID, "action_digest": approval.ActionDigest,
		"approval_decision": string(approval.Decision), "approver": approval.Approver,
	}
	if approval.Decision == ActionApprovalRejected {
		if _, err := w.applyLocked(Event{Type: EventExecutionIntentRejected, Actor: ActorHuman, Reason: approval.Reason, Metadata: metadata}); err != nil {
			return ActionApproval{}, err
		}
	} else if allRequiredActionsApproved(w.actionPolicies, w.actionApprovals) {
		if _, err := w.applyLocked(Event{Type: EventExecutionAuthorized, Actor: ActorHuman, Reason: "all required intent actions were approved", Metadata: metadata}); err != nil {
			return ActionApproval{}, err
		}
	}
	return *cloneActionApprovalPointer(findActionApproval(w.actionApprovals, approval.ActionID)), nil
}

func normalizeActionPolicyDecision(value *ActionPolicyDecision) {
	value.IntentID = strings.TrimSpace(value.IntentID)
	value.ActionID = strings.TrimSpace(value.ActionID)
	value.ActionDigest = strings.TrimSpace(value.ActionDigest)
	value.DryRunOperationID = strings.TrimSpace(value.DryRunOperationID)
	value.Risk = strings.ToLower(strings.TrimSpace(value.Risk))
	value.ReasonCode = strings.TrimSpace(value.ReasonCode)
	value.Reason = strings.TrimSpace(value.Reason)
}

func validateActionPolicyDecision(value ActionPolicyDecision) error {
	for field, content := range map[string]string{
		"intent_id": value.IntentID, "action_id": value.ActionID, "action_digest": value.ActionDigest,
		"dry_run_operation_id": value.DryRunOperationID, "risk": value.Risk, "reason_code": value.ReasonCode,
	} {
		if strings.TrimSpace(content) == "" {
			return fmt.Errorf("intent policy %s is required", field)
		}
	}
	switch value.Outcome {
	case ActionPolicyAutoApproved, ActionPolicyApprovalRequired, ActionPolicyRejected:
		return nil
	default:
		return fmt.Errorf("unknown intent policy outcome %q", value.Outcome)
	}
}

func normalizeActionApproval(value *ActionApproval) {
	value.IntentID = strings.TrimSpace(value.IntentID)
	value.ActionID = strings.TrimSpace(value.ActionID)
	value.ActionDigest = strings.TrimSpace(value.ActionDigest)
	value.Approver = strings.TrimSpace(value.Approver)
	value.Reason = strings.TrimSpace(value.Reason)
}

func validateActionApproval(value ActionApproval) error {
	for field, content := range map[string]string{
		"intent_id": value.IntentID, "action_id": value.ActionID, "action_digest": value.ActionDigest, "approver": value.Approver,
	} {
		if strings.TrimSpace(content) == "" {
			return fmt.Errorf("intent approval %s is required", field)
		}
	}
	switch value.Decision {
	case ActionApprovalApproved:
		return nil
	case ActionApprovalRejected:
		if value.Reason == "" {
			return fmt.Errorf("rejected intent action approval reason is required")
		}
		return nil
	default:
		return fmt.Errorf("unknown intent approval decision %q", value.Decision)
	}
}

func findActionPolicyDecision(values []ActionPolicyDecision, actionID string) *ActionPolicyDecision {
	for index := range values {
		if values[index].ActionID == actionID {
			return &values[index]
		}
	}
	return nil
}

func findActionApproval(values []ActionApproval, actionID string) *ActionApproval {
	for index := range values {
		if values[index].ActionID == actionID {
			return &values[index]
		}
	}
	return nil
}

func allRequiredActionsApproved(policies []ActionPolicyDecision, approvals []ActionApproval) bool {
	for _, policy := range policies {
		if policy.Outcome != ActionPolicyApprovalRequired {
			continue
		}
		approval := findActionApproval(approvals, policy.ActionID)
		if approval == nil || approval.Decision != ActionApprovalApproved || approval.ActionDigest != policy.ActionDigest {
			return false
		}
	}
	return true
}

func sameActionPolicyDecisions(left, right []ActionPolicyDecision) bool {
	if len(left) != len(right) {
		return false
	}
	for _, expected := range left {
		actual := findActionPolicyDecision(right, expected.ActionID)
		if actual == nil || expected.IntentID != actual.IntentID || expected.ActionDigest != actual.ActionDigest ||
			expected.DryRunOperationID != actual.DryRunOperationID || expected.Risk != actual.Risk ||
			expected.Outcome != actual.Outcome || expected.ReasonCode != actual.ReasonCode || expected.Reason != actual.Reason {
			return false
		}
	}
	return true
}

func cloneActionPolicyDecisions(values []ActionPolicyDecision) []ActionPolicyDecision {
	return append([]ActionPolicyDecision(nil), values...)
}

func cloneActionApprovals(values []ActionApproval) []ActionApproval {
	return append([]ActionApproval(nil), values...)
}

func cloneActionApprovalPointer(value *ActionApproval) *ActionApproval {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
