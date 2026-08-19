package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// IntendedAction 描述 ExecutionIntent 中一个已经冻结的修复动作。
// ID 和 Digest 由 Workflow 生成，审批必须同时绑定这两个字段。
type IntendedAction struct {
	ID                 string                           `json:"id"`
	Key                string                           `json:"key,omitempty"`
	Digest             string                           `json:"digest"`
	Kind               ActionKind                       `json:"kind"`
	ToolName           string                           `json:"tool_name"`
	Arguments          map[string]any                   `json:"arguments"`
	ArgumentReferences map[string]ActionOutputReference `json:"argument_references,omitempty"`
}

// ExecutionStageDraft 是 Agent 提交的一个短执行阶段。Stages 只允许线性排列；
// CheckpointPolicy 使用受限的结构化规则，不接受表达式或任意 JSONPath。
type ExecutionStageDraft struct {
	StageID          string           `json:"stage_id"`
	Goal             string           `json:"goal"`
	Actions          []IntendedAction `json:"actions"`
	SuccessCriteria  []string         `json:"success_criteria,omitempty"`
	CheckpointPolicy CheckpointPolicy `json:"checkpoint_policy,omitempty"`
	CreatedBy        string           `json:"created_by,omitempty"`
}

// ExecutionIntentDraft 是 Agent 基于当前观察提交的有界执行意图。
type ExecutionIntentDraft struct {
	Summary      string                `json:"summary"`
	RootCause    string                `json:"root_cause"`
	EvidenceRefs []string              `json:"evidence_refs"`
	Stages       []ExecutionStageDraft `json:"stages"`
}

// ExecutionIntent 是 Workflow 接收后生成 ID 并冻结的当前执行意图。
// 它不是一次性的全局计划；Checkpoint 可以用新观察唤回 Agent 提交新意图。
type ExecutionIntent struct {
	ID           string           `json:"id"`
	Summary      string           `json:"summary"`
	RootCause    string           `json:"root_cause"`
	EvidenceRefs []string         `json:"evidence_refs"`
	Stages       []ExecutionStage `json:"stages"`
	SubmittedAt  time.Time        `json:"submitted_at"`
}

// ExecutionIntentSubmission 返回已冻结的 ExecutionIntent 和它产生的状态转换。
type ExecutionIntentSubmission struct {
	ExecutionIntent ExecutionIntent `json:"intent"`
	Transition      Transition      `json:"transition"`
}

// SubmitExecutionIntent 原子校验 Agent 权限、冻结 ExecutionIntent，并把状态推进到 validating。
func (w *IncidentWorkflow) SubmitExecutionIntent(draft ExecutionIntentDraft) (ExecutionIntentSubmission, error) {
	if w == nil {
		return ExecutionIntentSubmission{}, fmt.Errorf("workflow is not initialized")
	}
	if err := validateExecutionIntentDraft(draft); err != nil {
		return ExecutionIntentSubmission{}, err
	}
	if err := validateDynamicExecutionIntentDraft(draft.Stages, w.limits); err != nil {
		return ExecutionIntentSubmission{}, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if err := authorizeAgentAction(w.state, AgentActionSubmitExecutionIntent); err != nil {
		return ExecutionIntentSubmission{}, err
	}
	actionCount := 0
	for _, stage := range draft.Stages {
		actionCount += len(stage.Actions)
	}
	if w.actionsAccepted+actionCount > w.limits.MaxActions {
		return ExecutionIntentSubmission{}, fmt.Errorf("run would exceed max_actions %d", w.limits.MaxActions)
	}
	stageCount := len(draft.Stages)
	if w.stagesExecuted+stageCount > w.limits.MaxStages {
		return ExecutionIntentSubmission{}, fmt.Errorf("run would exceed max_stages %d", w.limits.MaxStages)
	}
	intentID := fmt.Sprintf("%s-intent-%d", w.intentIDPrefix, w.version+1)
	transition, err := w.applyLocked(Event{
		Type: EventExecutionIntentSubmitted, Actor: ActorAgent,
		Metadata: map[string]string{"intent_id": intentID},
	})
	if err != nil {
		return ExecutionIntentSubmission{}, err
	}
	intent := ExecutionIntent{
		ID: intentID, Summary: strings.TrimSpace(draft.Summary), RootCause: strings.TrimSpace(draft.RootCause),
		EvidenceRefs: cloneStrings(draft.EvidenceRefs), Stages: freezeExecutionStages(intentID, draft.Stages, transition.At),
		SubmittedAt: transition.At,
	}
	w.intent = &intent
	w.actionsAccepted += actionCount
	w.executionIntents = append(w.executionIntents, *cloneExecutionIntentPointer(&intent))
	w.actionDryRuns = nil
	w.actionPolicies = nil
	w.actionApprovals = nil
	w.activeStageIndex = 0
	return ExecutionIntentSubmission{ExecutionIntent: *cloneExecutionIntentPointer(&intent), Transition: transition}, nil
}

func cloneExecutionIntents(values []ExecutionIntent) []ExecutionIntent {
	result := make([]ExecutionIntent, len(values))
	for index := range values {
		result[index] = *cloneExecutionIntentPointer(&values[index])
	}
	return result
}

func validateExecutionIntentDraft(draft ExecutionIntentDraft) error {
	required := map[string]string{"summary": draft.Summary, "root_cause": draft.RootCause}
	for field, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("intent %s is required", field)
		}
	}
	if len(draft.EvidenceRefs) == 0 {
		return fmt.Errorf("intent evidence_refs is required")
	}
	for _, reference := range draft.EvidenceRefs {
		if strings.TrimSpace(reference) == "" {
			return fmt.Errorf("intent evidence_refs must not contain empty values")
		}
	}
	if len(draft.Stages) == 0 {
		return fmt.Errorf("intent stages is required")
	}
	for index, stage := range draft.Stages {
		if strings.TrimSpace(stage.StageID) == "" {
			return fmt.Errorf("intent stages[%d].stage_id is required", index)
		}
		if strings.TrimSpace(stage.Goal) == "" {
			return fmt.Errorf("intent stages[%d].goal is required", index)
		}
		if len(stage.Actions) == 0 {
			return fmt.Errorf("intent stages[%d].actions is required", index)
		}
		for actionIndex, action := range stage.Actions {
			if strings.TrimSpace(action.ToolName) == "" {
				return fmt.Errorf("intent stages[%d].actions[%d].tool_name is required", index, actionIndex)
			}
		}
		if err := validateCheckpointPolicy(stage.CheckpointPolicy); err != nil {
			return fmt.Errorf("intent stages[%d].checkpoint_policy: %w", index, err)
		}
	}
	return nil
}

func cloneExecutionIntentPointer(value *ExecutionIntent) *ExecutionIntent {
	if value == nil {
		return nil
	}
	result := *value
	result.EvidenceRefs = cloneStrings(value.EvidenceRefs)
	result.Stages = cloneExecutionStages(value.Stages)
	return &result
}

func cloneIntendedAction(value IntendedAction) IntendedAction {
	return IntendedAction{ID: value.ID, Key: value.Key, Digest: value.Digest, Kind: value.Kind, ToolName: value.ToolName,
		Arguments: cloneAnyMap(value.Arguments), ArgumentReferences: cloneActionOutputReferences(value.ArgumentReferences)}
}

func cloneIntendedActions(values []IntendedAction) []IntendedAction {
	result := make([]IntendedAction, len(values))
	for index := range values {
		result[index] = cloneIntendedAction(values[index])
	}
	return result
}

func intendedActionDigest(action IntendedAction) string {
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
