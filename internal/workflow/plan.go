package workflow

import (
	"fmt"
	"strings"
	"time"
)

// PlannedAction 描述 Plan 中唯一允许执行的修复工具及其参数。
type PlannedAction struct {
	ToolName  string         `json:"tool_name"`
	Arguments map[string]any `json:"arguments"`
}

// PlanDraft 是 Agent 提交的结构化计划草案。
type PlanDraft struct {
	Summary          string        `json:"summary"`
	RootCause        string        `json:"root_cause"`
	EvidenceRefs     []string      `json:"evidence_refs"`
	Remediation      PlannedAction `json:"remediation"`
	ProbeRouteID     string        `json:"probe_route_id"`
	RecoveryPolicyID string        `json:"recovery_policy_id"`
}

// Plan 是 Workflow 接收后生成 ID 并冻结的计划。
type Plan struct {
	ID               string        `json:"id"`
	Summary          string        `json:"summary"`
	RootCause        string        `json:"root_cause"`
	EvidenceRefs     []string      `json:"evidence_refs"`
	Remediation      PlannedAction `json:"remediation"`
	ProbeRouteID     string        `json:"probe_route_id"`
	RecoveryPolicyID string        `json:"recovery_policy_id"`
	SubmittedAt      time.Time     `json:"submitted_at"`
}

// PlanSubmission 返回已冻结的 Plan 和它产生的状态转换。
type PlanSubmission struct {
	Plan       Plan       `json:"plan"`
	Transition Transition `json:"transition"`
}

// SubmitPlan 原子校验 Agent 权限、冻结 Plan，并把状态推进到 planned。
func (w *IncidentWorkflow) SubmitPlan(draft PlanDraft) (PlanSubmission, error) {
	if w == nil {
		return PlanSubmission{}, fmt.Errorf("workflow is not initialized")
	}
	if err := validatePlanDraft(draft); err != nil {
		return PlanSubmission{}, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if err := authorizeAgentAction(w.state, AgentActionSubmitPlan); err != nil {
		return PlanSubmission{}, err
	}

	planID := fmt.Sprintf("%s-plan-%d", w.incidentID, w.version+1)
	transition, err := w.applyLocked(Event{
		Type: EventPlanSubmitted, Actor: ActorAgent,
		Metadata: map[string]string{"plan_id": planID},
	})
	if err != nil {
		return PlanSubmission{}, err
	}
	plan := Plan{
		ID: planID, Summary: strings.TrimSpace(draft.Summary), RootCause: strings.TrimSpace(draft.RootCause),
		EvidenceRefs: cloneStrings(draft.EvidenceRefs), Remediation: clonePlannedAction(draft.Remediation),
		ProbeRouteID: strings.TrimSpace(draft.ProbeRouteID), RecoveryPolicyID: strings.TrimSpace(draft.RecoveryPolicyID),
		SubmittedAt: transition.At,
	}
	w.plan = &plan
	w.planDryRun = nil
	return PlanSubmission{Plan: *clonePlanPointer(&plan), Transition: transition}, nil
}

func validatePlanDraft(draft PlanDraft) error {
	required := map[string]string{
		"summary": draft.Summary, "root_cause": draft.RootCause,
		"remediation.tool_name": draft.Remediation.ToolName,
		"probe_route_id":        draft.ProbeRouteID, "recovery_policy_id": draft.RecoveryPolicyID,
	}
	for field, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("plan %s is required", field)
		}
	}
	if len(draft.EvidenceRefs) == 0 {
		return fmt.Errorf("plan evidence_refs is required")
	}
	for _, reference := range draft.EvidenceRefs {
		if strings.TrimSpace(reference) == "" {
			return fmt.Errorf("plan evidence_refs must not contain empty values")
		}
	}
	return nil
}

func clonePlanPointer(value *Plan) *Plan {
	if value == nil {
		return nil
	}
	result := *value
	result.EvidenceRefs = cloneStrings(value.EvidenceRefs)
	result.Remediation = clonePlannedAction(value.Remediation)
	return &result
}

func clonePlannedAction(value PlannedAction) PlannedAction {
	return PlannedAction{ToolName: value.ToolName, Arguments: cloneAnyMap(value.Arguments)}
}

func cloneStrings(values []string) []string {
	return append([]string(nil), values...)
}

func cloneAnyMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = cloneAny(item)
	}
	return result
}

func cloneAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneAnyMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index := range typed {
			result[index] = cloneAny(typed[index])
		}
		return result
	case []string:
		return cloneStrings(typed)
	default:
		return value
	}
}
