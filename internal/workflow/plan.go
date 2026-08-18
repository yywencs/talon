package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// PlannedAction 描述 Plan 中一个已经冻结的修复动作。
// ID 和 Digest 由 Workflow 生成，审批必须同时绑定这两个字段。
type PlannedAction struct {
	ID                 string                           `json:"id"`
	Key                string                           `json:"key,omitempty"`
	Digest             string                           `json:"digest"`
	Kind               ActionKind                       `json:"kind"`
	ToolName           string                           `json:"tool_name"`
	Arguments          map[string]any                   `json:"arguments"`
	ArgumentReferences map[string]ActionOutputReference `json:"argument_references,omitempty"`
}

// PlanStageDraft 是 Agent 提交的一个短执行阶段。Stages 只允许线性排列；
// CheckpointPolicy 使用受限的结构化规则，不接受表达式或任意 JSONPath。
type PlanStageDraft struct {
	StageID          string           `json:"stage_id"`
	Goal             string           `json:"goal"`
	Actions          []PlannedAction  `json:"actions"`
	SuccessCriteria  []string         `json:"success_criteria,omitempty"`
	CheckpointPolicy CheckpointPolicy `json:"checkpoint_policy,omitempty"`
	CreatedBy        string           `json:"created_by,omitempty"`
}

// PlanDraft 是 Agent 提交的结构化计划草案。
type PlanDraft struct {
	Summary      string           `json:"summary"`
	RootCause    string           `json:"root_cause"`
	EvidenceRefs []string         `json:"evidence_refs"`
	Stages       []PlanStageDraft `json:"stages"`
}

// Plan 是 Workflow 接收后生成 ID 并冻结的计划。
type Plan struct {
	ID           string      `json:"id"`
	Summary      string      `json:"summary"`
	RootCause    string      `json:"root_cause"`
	EvidenceRefs []string    `json:"evidence_refs"`
	Stages       []PlanStage `json:"stages"`
	SubmittedAt  time.Time   `json:"submitted_at"`
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
	if err := validateDynamicPlanDraft(draft.Stages, w.limits); err != nil {
		return PlanSubmission{}, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if err := authorizeAgentAction(w.state, AgentActionSubmitPlan); err != nil {
		return PlanSubmission{}, err
	}
	actionCount := 0
	for _, stage := range draft.Stages {
		actionCount += len(stage.Actions)
	}
	if w.actionsAccepted+actionCount > w.limits.MaxActions {
		return PlanSubmission{}, fmt.Errorf("run would exceed max_actions %d", w.limits.MaxActions)
	}
	stageCount := len(draft.Stages)
	if w.stagesExecuted+stageCount > w.limits.MaxStages {
		return PlanSubmission{}, fmt.Errorf("run would exceed max_stages %d", w.limits.MaxStages)
	}
	planID := fmt.Sprintf("%s-plan-%d", w.planIDPrefix, w.version+1)
	transition, err := w.applyLocked(Event{
		Type: EventPlanSubmitted, Actor: ActorAgent,
		Metadata: map[string]string{"plan_id": planID},
	})
	if err != nil {
		return PlanSubmission{}, err
	}
	plan := Plan{
		ID: planID, Summary: strings.TrimSpace(draft.Summary), RootCause: strings.TrimSpace(draft.RootCause),
		EvidenceRefs: cloneStrings(draft.EvidenceRefs), Stages: freezePlanStages(planID, draft.Stages, transition.At),
		SubmittedAt: transition.At,
	}
	w.plan = &plan
	w.actionsAccepted += actionCount
	w.plans = append(w.plans, *clonePlanPointer(&plan))
	w.planDryRuns = nil
	w.planPolicies = nil
	w.planApprovals = nil
	w.activeStageIndex = 0
	return PlanSubmission{Plan: *clonePlanPointer(&plan), Transition: transition}, nil
}

func clonePlans(values []Plan) []Plan {
	result := make([]Plan, len(values))
	for index := range values {
		result[index] = *clonePlanPointer(&values[index])
	}
	return result
}

func validatePlanDraft(draft PlanDraft) error {
	required := map[string]string{"summary": draft.Summary, "root_cause": draft.RootCause}
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
	if len(draft.Stages) == 0 {
		return fmt.Errorf("plan stages is required")
	}
	for index, stage := range draft.Stages {
		if strings.TrimSpace(stage.StageID) == "" {
			return fmt.Errorf("plan stages[%d].stage_id is required", index)
		}
		if strings.TrimSpace(stage.Goal) == "" {
			return fmt.Errorf("plan stages[%d].goal is required", index)
		}
		if len(stage.Actions) == 0 {
			return fmt.Errorf("plan stages[%d].actions is required", index)
		}
		for actionIndex, action := range stage.Actions {
			if strings.TrimSpace(action.ToolName) == "" {
				return fmt.Errorf("plan stages[%d].actions[%d].tool_name is required", index, actionIndex)
			}
		}
		if err := validateCheckpointPolicy(stage.CheckpointPolicy); err != nil {
			return fmt.Errorf("plan stages[%d].checkpoint_policy: %w", index, err)
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
	result.Stages = clonePlanStages(value.Stages)
	return &result
}

func clonePlannedAction(value PlannedAction) PlannedAction {
	return PlannedAction{ID: value.ID, Key: value.Key, Digest: value.Digest, Kind: value.Kind, ToolName: value.ToolName,
		Arguments: cloneAnyMap(value.Arguments), ArgumentReferences: cloneActionOutputReferences(value.ArgumentReferences)}
}

func clonePlannedActions(values []PlannedAction) []PlannedAction {
	result := make([]PlannedAction, len(values))
	for index := range values {
		result[index] = clonePlannedAction(values[index])
	}
	return result
}

func plannedActionDigest(action PlannedAction) string {
	payload, _ := json.Marshal(struct {
		Kind               ActionKind                       `json:"kind"`
		ToolName           string                           `json:"tool_name"`
		Arguments          map[string]any                   `json:"arguments"`
		ArgumentReferences map[string]ActionOutputReference `json:"argument_references,omitempty"`
	}{Kind: action.Kind, ToolName: action.ToolName, Arguments: action.Arguments, ArgumentReferences: action.ArgumentReferences})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
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
