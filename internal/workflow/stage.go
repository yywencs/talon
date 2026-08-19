package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"
)

const (
	DefaultMaxStages       = 16
	DefaultMaxAgentResumes = 8
	DefaultMaxActions      = 64
)

// ExecutionLimits 是 Harness 强制执行的循环和规模上限。
type ExecutionLimits struct {
	MaxStages       int `json:"max_stages"`
	MaxAgentResumes int `json:"max_agent_resumes"`
	MaxActions      int `json:"max_actions"`
}

type ActionOutputType string

const (
	ActionOutputString  ActionOutputType = "string"
	ActionOutputNumber  ActionOutputType = "number"
	ActionOutputInteger ActionOutputType = "integer"
	ActionOutputBoolean ActionOutputType = "boolean"
	ActionOutputObject  ActionOutputType = "object"
	ActionOutputArray   ActionOutputType = "array"
)

// ActionOutputReference 是一个显式、类型化的跨 Action 数据依赖。
// OutputPath 只支持由点分隔的对象字段，不支持数组、通配符、过滤器或脚本。
type ActionOutputReference struct {
	SourceActionID string           `json:"source_action_id"`
	OutputPath     string           `json:"output_path"`
	ExpectedType   ActionOutputType `json:"expected_type"`
	Required       bool             `json:"required"`
}

// ActionKind 标识冻结 Action 最终由哪一种受控平台写接口执行。
type ActionKind string

const (
	ActionKindRemediation ActionKind = "remediation"
	ActionKindProbe       ActionKind = "probe"
	ActionKindRecovery    ActionKind = "recovery"
)

func (k ActionKind) Valid() bool {
	switch k {
	case ActionKindRemediation, ActionKindProbe, ActionKindRecovery:
		return true
	default:
		return false
	}
}

type ResolvedArgumentSource struct {
	Argument       string                `json:"argument"`
	Reference      ActionOutputReference `json:"reference"`
	SourceResultID string                `json:"source_result_id"`
}

// ResolvedAction 保存模板参数和最终可执行参数。Digest 只覆盖解析后的具体动作，
// Dry Run、Policy 和审批都必须绑定这个 Digest。
type ResolvedAction struct {
	IntentID          string                   `json:"intent_id"`
	StageID           string                   `json:"stage_id"`
	ActionID          string                   `json:"action_id"`
	TemplateDigest    string                   `json:"template_digest"`
	Digest            string                   `json:"digest"`
	Kind              ActionKind               `json:"kind"`
	ToolName          string                   `json:"tool_name"`
	OriginalArguments map[string]any           `json:"original_arguments"`
	Arguments         map[string]any           `json:"arguments"`
	Sources           []ResolvedArgumentSource `json:"sources"`
	ResolvedAt        time.Time                `json:"resolved_at"`
}

// ActionResult 是 Harness 保存的结构化执行观察，也是后续引用的唯一数据源。
type ActionResult struct {
	ResultID        string         `json:"result_id"`
	IntentID        string         `json:"intent_id"`
	StageID         string         `json:"stage_id"`
	ActionID        string         `json:"action_id"`
	ActionDigest    string         `json:"action_digest"`
	OperationID     string         `json:"operation_id"`
	OperationStatus string         `json:"operation_status"`
	Output          map[string]any `json:"output"`
	EvidenceRef     string         `json:"evidence_ref"`
	RecordedAt      time.Time      `json:"recorded_at"`
}

type CheckpointDecision string

const (
	CheckpointContinue   CheckpointDecision = "continue"
	CheckpointNeedsAgent CheckpointDecision = "needs_agent"
	CheckpointSucceeded  CheckpointDecision = "succeeded"
	CheckpointFailed     CheckpointDecision = "failed"
	CheckpointEscalate   CheckpointDecision = "escalate"
	CheckpointBlocked    CheckpointDecision = "blocked"
)

// CheckpointRule 只允许对一个已知输出字段做精确相等判断。
type CheckpointRule struct {
	SourceActionID string             `json:"source_action_id" jsonschema:"required,description=本 ExecutionIntent 中已执行 Action 的稳定 ID"`
	OutputPath     string             `json:"output_path" jsonschema:"required,description=只允许 operation_status 或 output.<field>；operation_status 和 output.outcome 都是字符串"`
	Equals         any                `json:"equals" jsonschema:"required,description=与字段 JSON 类型一致的精确字面值；operation_status 使用 succeeded/failed/rejected/cancelled 等字符串，output.outcome 使用 healthy/hard_stop/running 等字符串，不能写布尔 true"`
	Decision       CheckpointDecision `json:"decision"`
	Reason         string             `json:"reason,omitempty"`
	NextStageID    string             `json:"next_stage_id,omitempty"`
}

type CheckpointPolicy struct {
	Rules           []CheckpointRule   `json:"rules,omitempty"`
	DefaultDecision CheckpointDecision `json:"default_decision,omitempty"`
	DefaultReason   string             `json:"default_reason,omitempty"`
}

type ExecutionStage struct {
	StageID          string           `json:"stage_id"`
	Goal             string           `json:"goal"`
	Sequence         int              `json:"sequence"`
	Version          int              `json:"version"`
	Actions          []IntendedAction `json:"actions"`
	SuccessCriteria  []string         `json:"success_criteria,omitempty"`
	CheckpointPolicy CheckpointPolicy `json:"checkpoint_policy"`
	CreatedBy        string           `json:"created_by"`
	CreatedAt        time.Time        `json:"created_at"`
}

type DecisionCheckpoint struct {
	CheckpointID    string             `json:"checkpoint_id"`
	StageID         string             `json:"stage_id"`
	Trigger         string             `json:"trigger"`
	LatestResults   []ActionResult     `json:"latest_results"`
	NewEvidenceRefs []string           `json:"new_evidence_refs"`
	Decision        CheckpointDecision `json:"decision"`
	DecisionReason  string             `json:"decision_reason"`
	NextStageID     string             `json:"next_stage_id,omitempty"`
	CreatedAt       time.Time          `json:"created_at"`
}

var (
	stageIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	outputField     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)
)

func normalizeLimits(value ExecutionLimits) ExecutionLimits {
	if value.MaxStages == 0 {
		value.MaxStages = DefaultMaxStages
	}
	if value.MaxAgentResumes == 0 {
		value.MaxAgentResumes = DefaultMaxAgentResumes
	}
	if value.MaxActions == 0 {
		value.MaxActions = DefaultMaxActions
	}
	return value
}

func validateLimits(value ExecutionLimits) error {
	if value.MaxStages <= 0 || value.MaxAgentResumes <= 0 || value.MaxActions <= 0 {
		return fmt.Errorf("workflow execution limits must be positive")
	}
	return nil
}

func validateCheckpointPolicy(policy CheckpointPolicy) error {
	if policy.DefaultDecision != "" && !policy.DefaultDecision.valid() {
		return fmt.Errorf("unknown default decision %q", policy.DefaultDecision)
	}
	for index, rule := range policy.Rules {
		if strings.TrimSpace(rule.SourceActionID) == "" {
			return fmt.Errorf("rules[%d].source_action_id is required", index)
		}
		if err := validateOutputPath(rule.OutputPath); err != nil {
			return fmt.Errorf("rules[%d].output_path: %w", index, err)
		}
		if err := validateCheckpointEquals(rule.OutputPath, rule.Equals); err != nil {
			return fmt.Errorf("rules[%d].equals: %w", index, err)
		}
		if !rule.Decision.valid() {
			return fmt.Errorf("rules[%d] has unknown decision %q", index, rule.Decision)
		}
	}
	return nil
}

func validateCheckpointEquals(path string, value any) error {
	path = strings.TrimSpace(path)
	switch path {
	case "operation_status", "output.outcome", "output.route_id", "output.policy_id":
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return fmt.Errorf("%s requires a non-empty string comparison value", path)
		}
		return nil
	case "output.applied", "output.validated", "output.telemetry_complete":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s requires a boolean comparison value", path)
		}
		return nil
	}
	switch value.(type) {
	case string, bool, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		return nil
	default:
		return fmt.Errorf("checkpoint comparison must be a scalar JSON value")
	}
}

func validateDynamicExecutionIntentDraft(stages []ExecutionStageDraft, limits ExecutionLimits) error {
	if len(stages) > limits.MaxStages {
		return fmt.Errorf("intent has %d stages, exceeding max_stages %d", len(stages), limits.MaxStages)
	}
	stageIDs := make(map[string]struct{}, len(stages))
	actionKeys := make(map[string]struct{})
	actionStages := make(map[string]int)
	actionCount := 0
	for stageIndex, stage := range stages {
		stageID := strings.TrimSpace(stage.StageID)
		if !stageIdentifier.MatchString(stageID) {
			return fmt.Errorf("intent stages[%d].stage_id uses an invalid identifier", stageIndex)
		}
		if _, exists := stageIDs[stageID]; exists {
			return fmt.Errorf("duplicate stage_id %q", stageID)
		}
		stageIDs[stageID] = struct{}{}
		for actionIndex, action := range stage.Actions {
			actionCount++
			if action.Kind != "" && !action.Kind.Valid() {
				return fmt.Errorf("intent stages[%d].actions[%d] has unknown kind %q", stageIndex, actionIndex, action.Kind)
			}
			key := strings.TrimSpace(action.Key)
			if key == "" {
				key = strings.TrimSpace(action.ID)
			}
			if key != "" {
				if !stageIdentifier.MatchString(key) {
					return fmt.Errorf("intent stages[%d].actions[%d] key uses an invalid identifier", stageIndex, actionIndex)
				}
				if _, exists := actionKeys[key]; exists {
					return fmt.Errorf("duplicate action key %q", key)
				}
				actionKeys[key] = struct{}{}
				actionStages[key] = stageIndex
			}
			for argument, reference := range action.ArgumentReferences {
				if !outputField.MatchString(argument) {
					return fmt.Errorf("intent stages[%d].actions[%d] argument reference target %q is invalid", stageIndex, actionIndex, argument)
				}
				if err := validateActionOutputReference(reference); err != nil {
					return fmt.Errorf("intent stages[%d].actions[%d] argument %q: %w", stageIndex, actionIndex, argument, err)
				}
				if _, literal := action.Arguments[argument]; literal {
					return fmt.Errorf("intent stages[%d].actions[%d] argument %q cannot have both a literal and an output reference",
						stageIndex, actionIndex, argument)
				}
			}
		}
	}
	if actionCount > limits.MaxActions {
		return fmt.Errorf("intent has %d actions, exceeding max_actions %d", actionCount, limits.MaxActions)
	}
	for stageIndex, stage := range stages {
		if err := validateProbeStageCheckpoint(stageIndex, stage, stages); err != nil {
			return err
		}
		for actionIndex, action := range stage.Actions {
			for argument, reference := range action.ArgumentReferences {
				sourceStage, exists := actionStages[strings.TrimSpace(reference.SourceActionID)]
				if !exists {
					return fmt.Errorf("intent stages[%d].actions[%d] argument %q references unknown action %q",
						stageIndex, actionIndex, argument, reference.SourceActionID)
				}
				if sourceStage >= stageIndex {
					return fmt.Errorf("intent stages[%d].actions[%d] argument %q must reference an earlier stage action",
						stageIndex, actionIndex, argument)
				}
			}
		}
		for ruleIndex, rule := range stage.CheckpointPolicy.Rules {
			sourceStage, exists := actionStages[strings.TrimSpace(rule.SourceActionID)]
			if !exists || sourceStage > stageIndex {
				return fmt.Errorf("intent stages[%d].checkpoint_policy.rules[%d] references unavailable action %q",
					stageIndex, ruleIndex, rule.SourceActionID)
			}
			nextStageID := strings.TrimSpace(rule.NextStageID)
			if rule.Decision == CheckpointContinue {
				if stageIndex+1 >= len(stages) {
					return fmt.Errorf("intent stages[%d].checkpoint_policy.rules[%d] cannot continue from the final stage; use succeeded or another terminal decision",
						stageIndex, ruleIndex)
				}
				if nextStageID != "" && nextStageID != strings.TrimSpace(stages[stageIndex+1].StageID) {
					return fmt.Errorf("intent stages[%d].checkpoint_policy.rules[%d] next_stage_id must name the next linear stage",
						stageIndex, ruleIndex)
				}
			} else if nextStageID != "" {
				return fmt.Errorf("intent stages[%d].checkpoint_policy.rules[%d] next_stage_id is only allowed for continue",
					stageIndex, ruleIndex)
			}
		}
		if stage.CheckpointPolicy.DefaultDecision == CheckpointContinue && stageIndex+1 >= len(stages) {
			return fmt.Errorf("intent stages[%d].checkpoint_policy cannot default to continue on the final stage; use succeeded or another terminal decision", stageIndex)
		}
	}
	return nil
}

func validateProbeStageCheckpoint(stageIndex int, stage ExecutionStageDraft, stages []ExecutionStageDraft) error {
	probeActions := make(map[string]struct{})
	for actionIndex, action := range stage.Actions {
		if action.Kind != ActionKindProbe && strings.TrimSpace(action.ToolName) != "request_probe" {
			continue
		}
		key := strings.TrimSpace(action.Key)
		if key == "" {
			key = strings.TrimSpace(action.ID)
		}
		if key == "" {
			return fmt.Errorf("intent stages[%d].actions[%d] request_probe requires a stable action id for checkpoint rules", stageIndex, actionIndex)
		}
		probeActions[key] = struct{}{}
	}
	if len(probeActions) == 0 {
		return nil
	}
	if !failClosedCheckpointDecision(stage.CheckpointPolicy.DefaultDecision) {
		return fmt.Errorf("intent stages[%d].checkpoint_policy for request_probe requires an explicit fail-closed default_decision (needs_agent, failed, escalate, or blocked)", stageIndex)
	}
	if stageIndex+1 >= len(stages) || !stageContainsManagedRecovery(stages[stageIndex+1]) {
		return fmt.Errorf("intent stages[%d] containing request_probe requires the next linear stage to contain an explicit request_recovery action", stageIndex)
	}
	healthyProgress := make(map[string]bool, len(probeActions))
	for ruleIndex, rule := range stage.CheckpointPolicy.Rules {
		if rule.Decision == CheckpointSucceeded {
			return fmt.Errorf("intent stages[%d].checkpoint_policy.rules[%d] cannot select succeeded for a probe stage; a healthy probe must continue to an explicit recovery stage", stageIndex, ruleIndex)
		}
		if rule.Decision != CheckpointContinue {
			continue
		}
		_, isProbe := probeActions[strings.TrimSpace(rule.SourceActionID)]
		outcome, isString := rule.Equals.(string)
		if !isProbe || strings.TrimSpace(rule.OutputPath) != "output.outcome" || !isString || outcome != "healthy" {
			return fmt.Errorf("intent stages[%d].checkpoint_policy.rules[%d] cannot select %q for a probe stage unless the current probe output.outcome equals healthy", stageIndex, ruleIndex, rule.Decision)
		}
		healthyProgress[strings.TrimSpace(rule.SourceActionID)] = true
	}
	for actionID := range probeActions {
		if !healthyProgress[actionID] {
			return fmt.Errorf("intent stages[%d].checkpoint_policy must define a healthy output.outcome continue rule to an explicit recovery stage for probe action %q", stageIndex, actionID)
		}
	}
	return nil
}

func stageContainsManagedRecovery(stage ExecutionStageDraft) bool {
	for _, action := range stage.Actions {
		if action.Kind == ActionKindRecovery || strings.TrimSpace(action.ToolName) == "request_recovery" {
			return true
		}
	}
	return false
}

func failClosedCheckpointDecision(value CheckpointDecision) bool {
	switch value {
	case CheckpointNeedsAgent, CheckpointFailed, CheckpointEscalate, CheckpointBlocked:
		return true
	default:
		return false
	}
}

func validateActionOutputReference(value ActionOutputReference) error {
	if strings.TrimSpace(value.SourceActionID) == "" {
		return fmt.Errorf("source_action_id is required")
	}
	if err := validateOutputPath(value.OutputPath); err != nil {
		return err
	}
	if !value.ExpectedType.valid() {
		return fmt.Errorf("unknown expected_type %q", value.ExpectedType)
	}
	if strings.TrimSpace(value.OutputPath) == "operation_status" && value.ExpectedType != ActionOutputString {
		return fmt.Errorf("operation_status requires expected_type %q", ActionOutputString)
	}
	return nil
}

func validateOutputPath(value string) error {
	value = strings.TrimSpace(value)
	if value == "operation_status" {
		return nil
	}
	parts := strings.Split(value, ".")
	if len(parts) < 2 || len(parts) > 9 || parts[0] != "output" {
		return fmt.Errorf("output_path must be operation_status or output.<field> with at most 8 fields")
	}
	for _, part := range parts[1:] {
		if !outputField.MatchString(part) {
			return fmt.Errorf("output_path %q is not a restricted field path", value)
		}
	}
	return nil
}

func (t ActionOutputType) valid() bool {
	switch t {
	case ActionOutputString, ActionOutputNumber, ActionOutputInteger, ActionOutputBoolean, ActionOutputObject, ActionOutputArray:
		return true
	default:
		return false
	}
}

func (d CheckpointDecision) valid() bool {
	switch d {
	case CheckpointContinue, CheckpointNeedsAgent, CheckpointSucceeded, CheckpointFailed, CheckpointEscalate, CheckpointBlocked:
		return true
	default:
		return false
	}
}

func freezeExecutionStages(intentID string, drafts []ExecutionStageDraft, createdAt time.Time) []ExecutionStage {
	actionIDs := make(map[string]string)
	sequence := 0
	result := make([]ExecutionStage, len(drafts))
	for stageIndex, draft := range drafts {
		stage := ExecutionStage{
			StageID: strings.TrimSpace(draft.StageID), Goal: strings.TrimSpace(draft.Goal), Sequence: stageIndex + 1,
			Version: 1, SuccessCriteria: cloneStrings(draft.SuccessCriteria),
			CheckpointPolicy: cloneCheckpointPolicy(draft.CheckpointPolicy), CreatedBy: strings.TrimSpace(draft.CreatedBy), CreatedAt: createdAt,
		}
		if stage.CreatedBy == "" {
			stage.CreatedBy = string(ActorAgent)
		}
		stage.Actions = make([]IntendedAction, len(draft.Actions))
		for actionIndex, draftAction := range draft.Actions {
			sequence++
			action := cloneIntendedAction(draftAction)
			if action.Kind == "" {
				action.Kind = ActionKindRemediation
			}
			key := strings.TrimSpace(action.Key)
			if key == "" {
				key = strings.TrimSpace(action.ID)
			}
			action.Key = key
			action.ID = fmt.Sprintf("%s-action-%d", intentID, sequence)
			if key != "" {
				actionIDs[key] = action.ID
			}
			action.ToolName = strings.TrimSpace(action.ToolName)
			stage.Actions[actionIndex] = action
		}
		result[stageIndex] = stage
	}
	for stageIndex := range result {
		for actionIndex := range result[stageIndex].Actions {
			action := &result[stageIndex].Actions[actionIndex]
			for argument, reference := range action.ArgumentReferences {
				if canonical := actionIDs[strings.TrimSpace(reference.SourceActionID)]; canonical != "" {
					reference.SourceActionID = canonical
				}
				reference.OutputPath = strings.TrimSpace(reference.OutputPath)
				action.ArgumentReferences[argument] = reference
			}
			action.Digest = intendedActionDigest(*action)
		}
		for ruleIndex := range result[stageIndex].CheckpointPolicy.Rules {
			rule := &result[stageIndex].CheckpointPolicy.Rules[ruleIndex]
			if canonical := actionIDs[strings.TrimSpace(rule.SourceActionID)]; canonical != "" {
				rule.SourceActionID = canonical
			}
		}
	}
	return result
}

func cloneExecutionStages(values []ExecutionStage) []ExecutionStage {
	result := make([]ExecutionStage, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Actions = cloneIntendedActions(value.Actions)
		result[index].SuccessCriteria = cloneStrings(value.SuccessCriteria)
		result[index].CheckpointPolicy = cloneCheckpointPolicy(value.CheckpointPolicy)
	}
	return result
}

func cloneCheckpointPolicy(value CheckpointPolicy) CheckpointPolicy {
	result := value
	result.Rules = make([]CheckpointRule, len(value.Rules))
	for index, rule := range value.Rules {
		result.Rules[index] = rule
		result.Rules[index].Equals = cloneAny(rule.Equals)
	}
	return result
}

func cloneActionOutputReferences(values map[string]ActionOutputReference) map[string]ActionOutputReference {
	if values == nil {
		return nil
	}
	result := make(map[string]ActionOutputReference, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func (w *IncidentWorkflow) currentStageLocked() *ExecutionStage {
	if w.intent == nil || len(w.intent.Stages) == 0 || w.activeStageIndex < 0 || w.activeStageIndex >= len(w.intent.Stages) {
		return nil
	}
	return &w.intent.Stages[w.activeStageIndex]
}

func currentStage(snapshot Snapshot) *ExecutionStage {
	if snapshot.ExecutionIntent == nil || len(snapshot.ExecutionIntent.Stages) == 0 || snapshot.ActiveStageIndex < 0 || snapshot.ActiveStageIndex >= len(snapshot.ExecutionIntent.Stages) {
		return nil
	}
	return &snapshot.ExecutionIntent.Stages[snapshot.ActiveStageIndex]
}

// CurrentStage 返回当前 ExecutionIntent 的活动 Stage。
func CurrentStage(snapshot Snapshot) *ExecutionStage {
	stage := currentStage(snapshot)
	if stage == nil {
		return nil
	}
	cloned := cloneExecutionStages([]ExecutionStage{*stage})
	return &cloned[0]
}

// ExecutableActions 返回当前 Stage 已解析的具体 Action。
func ExecutableActions(snapshot Snapshot) []IntendedAction {
	if snapshot.ExecutionIntent == nil {
		return nil
	}
	stage := currentStage(snapshot)
	if stage == nil {
		return nil
	}
	result := make([]IntendedAction, 0, len(stage.Actions))
	for _, template := range stage.Actions {
		resolved := findResolvedAction(snapshot.ResolvedActions, snapshot.ExecutionIntent.ID, stage.StageID, template.ID)
		if resolved == nil {
			continue
		}
		result = append(result, IntendedAction{ID: resolved.ActionID, Key: template.Key, Digest: resolved.Digest,
			Kind: resolved.Kind, ToolName: resolved.ToolName, Arguments: cloneAnyMap(resolved.Arguments)})
	}
	return result
}

func (w *IncidentWorkflow) executableActionsLocked() []IntendedAction {
	if w.intent == nil {
		return nil
	}
	stage := w.currentStageLocked()
	if stage == nil {
		return nil
	}
	result := make([]IntendedAction, 0, len(stage.Actions))
	for _, template := range stage.Actions {
		resolved := findResolvedAction(w.resolvedActions, w.intent.ID, stage.StageID, template.ID)
		if resolved == nil {
			continue
		}
		result = append(result, IntendedAction{ID: resolved.ActionID, Key: template.Key, Digest: resolved.Digest,
			Kind: resolved.Kind, ToolName: resolved.ToolName, Arguments: cloneAnyMap(resolved.Arguments)})
	}
	return result
}

// ResolveCurrentStage 把当前 Stage 的引用解析为具体参数。解析失败会产生规范化
// StageFailure 和 needs_agent Checkpoint，并在任何 Dry Run/Policy/审批发生前停止当前 ExecutionIntent。
func (w *IncidentWorkflow) ResolveCurrentStage() ([]ResolvedAction, error) {
	if w == nil {
		return nil, fmt.Errorf("workflow is not initialized")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state != StateValidating || w.intent == nil {
		return nil, fmt.Errorf("%w: action resolution is not allowed in state %q", ErrInvalidTransition, w.state)
	}
	stage := w.currentStageLocked()
	if stage == nil {
		return nil, fmt.Errorf("validating workflow has no active stage")
	}
	existing := resolvedActionsForStage(w.resolvedActions, w.intent.ID, stage.StageID)
	if len(existing) == len(stage.Actions) {
		return cloneResolvedActions(existing), nil
	}
	resolved := make([]ResolvedAction, 0, len(stage.Actions))
	for _, template := range stage.Actions {
		arguments := cloneAnyMap(template.Arguments)
		if arguments == nil {
			arguments = make(map[string]any)
		}
		sources := make([]ResolvedArgumentSource, 0, len(template.ArgumentReferences))
		for argument, reference := range template.ArgumentReferences {
			result := findActionResult(w.actionResults, reference.SourceActionID)
			if result == nil {
				if !reference.Required {
					continue
				}
				return nil, w.failResolutionLocked(stage.StageID, template.ID, "required_action_output_missing",
					"后续 Action 所需的前序结构化输出不存在", fmt.Sprintf("source action %q has no result", reference.SourceActionID))
			}
			value, exists := lookupActionResultPath(*result, reference.OutputPath)
			if !exists {
				if !reference.Required {
					continue
				}
				return nil, w.failResolutionLocked(stage.StageID, template.ID, "required_output_field_missing",
					"后续 Action 所需的结构化输出字段不存在", fmt.Sprintf("output path %q is missing", reference.OutputPath))
			}
			if !matchesOutputType(value, reference.ExpectedType) {
				return nil, w.failResolutionLocked(stage.StageID, template.ID, "action_output_type_mismatch",
					"前序 Action 输出字段类型与引用声明不匹配", fmt.Sprintf("output path %q does not match expected type %q", reference.OutputPath, reference.ExpectedType))
			}
			arguments[argument] = cloneAny(value)
			sources = append(sources, ResolvedArgumentSource{Argument: argument, Reference: reference, SourceResultID: result.ResultID})
		}
		concrete := IntendedAction{ID: template.ID, Key: template.Key, Kind: template.Kind, ToolName: template.ToolName, Arguments: arguments}
		resolved = append(resolved, ResolvedAction{
			IntentID: w.intent.ID, StageID: stage.StageID, ActionID: template.ID, TemplateDigest: template.Digest,
			Digest: intendedActionDigest(concrete), Kind: template.Kind, ToolName: template.ToolName,
			OriginalArguments: cloneAnyMap(template.Arguments), Arguments: arguments, Sources: sources, ResolvedAt: w.now(),
		})
	}
	w.resolvedActions = append(w.resolvedActions, cloneResolvedActions(resolved)...)
	return cloneResolvedActions(resolved), nil
}

func (w *IncidentWorkflow) failResolutionLocked(stageID, actionID, code, summary, message string) error {
	failure := StageFailure{
		Stage: FailureStageArgumentResolution, Category: FailureCategoryIntentInvalid, Code: code,
		SafeSummary: summary, Message: message, NextAction: FailureNextNeedsAgent,
		IntentID: w.intent.ID, ActionID: actionID,
	}
	decision, eventType, checkpointReason := w.needsAgentDecisionLocked(summary)
	checkpoint := w.newCheckpointLocked(stageID, "argument_resolution", decision, checkpointReason, "")
	w.checkpoints = append(w.checkpoints, checkpoint)
	_, err := w.applyLocked(Event{Type: eventType, Actor: ActorWorkflow, Reason: summary,
		Metadata: map[string]string{"intent_id": w.intent.ID, "stage_id": stageID, "checkpoint_id": checkpoint.CheckpointID}, Failure: &failure})
	if err != nil {
		return err
	}
	return fmt.Errorf("%s: %s", code, message)
}

// RecordActionResult 保存执行成功后的结构化输出，供后续 Stage 引用。
func (w *IncidentWorkflow) RecordActionResult(value ActionResult) (ActionResult, error) {
	if w == nil {
		return ActionResult{}, fmt.Errorf("workflow is not initialized")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state != StateExecuting {
		return ActionResult{}, fmt.Errorf("%w: action result is not allowed in state %q", ErrInvalidTransition, w.state)
	}
	if w.intent == nil || value.IntentID != w.intent.ID {
		return ActionResult{}, fmt.Errorf("action result does not match current intent")
	}
	if existing := findActionResult(w.actionResults, value.ActionID); existing != nil {
		if existing.ActionDigest != value.ActionDigest || existing.OperationID != value.OperationID {
			return ActionResult{}, fmt.Errorf("action result is already bound to different immutable content")
		}
		return cloneActionResult(*existing), nil
	}
	resolved := findResolvedAction(w.resolvedActions, value.IntentID, value.StageID, value.ActionID)
	if resolved == nil || resolved.Digest != value.ActionDigest {
		return ActionResult{}, fmt.Errorf("action result does not match a resolved action")
	}
	value.OperationID = strings.TrimSpace(value.OperationID)
	value.OperationStatus = strings.TrimSpace(value.OperationStatus)
	value.Output = cloneAnyMap(value.Output)
	value.RecordedAt = w.now()
	value.ResultID = value.ActionID + ":result"
	payload, _ := json.Marshal(value.Output)
	sum := sha256.Sum256(append([]byte(value.ActionID+"\x00"), payload...))
	value.EvidenceRef = "action:" + value.ActionID + ":" + hex.EncodeToString(sum[:8])
	w.actionResults = append(w.actionResults, cloneActionResult(value))
	return cloneActionResult(value), nil
}

// CompleteCurrentStage 在当前 Stage 执行结束后进入 Checkpoint。
func (w *IncidentWorkflow) CompleteCurrentStage(reason string) (Transition, error) {
	if w == nil {
		return Transition{}, fmt.Errorf("workflow is not initialized")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.intent == nil {
		return Transition{}, fmt.Errorf("executing workflow has no intent")
	}
	stage := w.currentStageLocked()
	if stage == nil {
		return Transition{}, fmt.Errorf("executing workflow has no active stage")
	}
	transition, err := w.applyLocked(Event{Type: EventStageCheckpoint, Actor: ActorWorkflow, Reason: reason,
		Metadata: map[string]string{"intent_id": w.intent.ID, "stage_id": stage.StageID}})
	if err == nil {
		w.stagesExecuted++
	}
	return transition, err
}

// FailCurrentStage 把当前 Stage 的执行失败收敛为显式 Checkpoint。
func (w *IncidentWorkflow) FailCurrentStage(reason string, metadata map[string]string, failure StageFailure) (Transition, error) {
	if w == nil {
		return Transition{}, fmt.Errorf("workflow is not initialized")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.intent == nil || w.currentStageLocked() == nil {
		return Transition{}, fmt.Errorf("executing workflow has no active stage")
	}
	stage := w.currentStageLocked()
	decision, eventType, checkpointReason := w.needsAgentDecisionLocked(failure.SafeSummary)
	if failure.Category == FailureCategoryAuthorizationRequired {
		decision, eventType = CheckpointBlocked, EventCheckpointBlocked
	} else if failure.NextAction == FailureNextEscalate {
		decision, eventType = CheckpointEscalate, EventCheckpointEscalated
	} else if failure.NextAction == FailureNextRetry {
		decision, eventType = CheckpointFailed, EventCheckpointFailed
	}
	checkpoint := w.newCheckpointLocked(stage.StageID, "stage_failed", decision, checkpointReason, "")
	w.checkpoints = append(w.checkpoints, checkpoint)
	checkpointMetadata := cloneMetadata(metadata)
	if checkpointMetadata == nil {
		checkpointMetadata = make(map[string]string)
	}
	checkpointMetadata["stage_id"] = stage.StageID
	checkpointMetadata["checkpoint_id"] = checkpoint.CheckpointID
	checkpointMetadata["checkpoint_decision"] = string(decision)
	return w.applyLocked(Event{Type: eventType, Actor: ActorWorkflow, Reason: reason, Metadata: checkpointMetadata, Failure: &failure})
}

// EvaluateCheckpoint 根据当前 Stage 的精确匹配规则产生确定性决定。
func (w *IncidentWorkflow) EvaluateCheckpoint() (DecisionCheckpoint, error) {
	if w == nil {
		return DecisionCheckpoint{}, fmt.Errorf("workflow is not initialized")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state != StateCheckpoint || w.intent == nil || w.currentStageLocked() == nil {
		return DecisionCheckpoint{}, fmt.Errorf("%w: checkpoint evaluation is not allowed in state %q", ErrInvalidTransition, w.state)
	}
	stage := w.currentStageLocked()
	decision := stage.CheckpointPolicy.DefaultDecision
	reason := strings.TrimSpace(stage.CheckpointPolicy.DefaultReason)
	nextStageID := ""
	for _, rule := range stage.CheckpointPolicy.Rules {
		result := findActionResult(w.actionResults, rule.SourceActionID)
		if result == nil {
			continue
		}
		value, exists := lookupActionResultPath(*result, rule.OutputPath)
		if exists && reflect.DeepEqual(normalizeComparable(value), normalizeComparable(rule.Equals)) {
			decision, reason, nextStageID = rule.Decision, strings.TrimSpace(rule.Reason), strings.TrimSpace(rule.NextStageID)
			break
		}
	}
	if decision == "" {
		if w.activeStageIndex+1 < len(w.intent.Stages) {
			decision = CheckpointContinue
			nextStageID = w.intent.Stages[w.activeStageIndex+1].StageID
			reason = "current stage completed and the next linear stage is available"
		} else {
			decision = CheckpointSucceeded
			reason = "final stage completed successfully"
		}
	}
	if reason == "" {
		reason = "checkpoint policy selected " + string(decision)
	}
	if decision == CheckpointContinue {
		if w.activeStageIndex+1 >= len(w.intent.Stages) {
			decision, reason = CheckpointNeedsAgent, "checkpoint requested continue but no next stage exists"
		} else {
			expected := w.intent.Stages[w.activeStageIndex+1].StageID
			if nextStageID == "" {
				nextStageID = expected
			}
			if nextStageID != expected {
				decision, reason, nextStageID = CheckpointNeedsAgent, "checkpoint next_stage_id violates linear stage ordering", ""
			} else if w.stagesExecuted >= w.limits.MaxStages {
				decision, reason, nextStageID = CheckpointFailed, "maximum stage count exceeded", ""
			}
		}
	}
	eventType := EventType("")
	if decision == CheckpointNeedsAgent {
		decision, eventType, reason = w.needsAgentDecisionLocked(reason)
	}
	checkpoint := w.newCheckpointLocked(stage.StageID, "stage_completed", decision, reason, nextStageID)
	w.checkpoints = append(w.checkpoints, checkpoint)
	event := Event{Type: eventType, Actor: ActorWorkflow, Reason: reason, Metadata: map[string]string{
		"intent_id": w.intent.ID, "stage_id": stage.StageID, "checkpoint_id": checkpoint.CheckpointID,
		"checkpoint_decision": string(decision), "next_stage_id": nextStageID,
	}}
	switch decision {
	case CheckpointContinue:
		event.Type = EventCheckpointContinue
		w.activeStageIndex++
		w.actionDryRuns, w.actionPolicies, w.actionApprovals = nil, nil, nil
	case CheckpointNeedsAgent:
		if event.Type == "" {
			event.Type = EventCheckpointNeedsAgent
		}
	case CheckpointSucceeded:
		event.Type = EventCheckpointSucceeded
	case CheckpointFailed:
		event.Type = EventCheckpointFailed
		event.Failure = &StageFailure{Stage: FailureStageCheckpoint, Category: FailureCategoryExecutionFailed,
			Code: "checkpoint_failed", SafeSummary: reason, NextAction: FailureNextEscalate, IntentID: w.intent.ID}
	case CheckpointEscalate:
		event.Type = EventCheckpointEscalated
	case CheckpointBlocked:
		event.Type = EventCheckpointBlocked
	}
	if _, err := w.applyLocked(event); err != nil {
		return DecisionCheckpoint{}, err
	}
	return cloneDecisionCheckpoint(checkpoint), nil
}

func (w *IncidentWorkflow) needsAgentDecisionLocked(reason string) (CheckpointDecision, EventType, string) {
	if w.agentResumesUsed >= w.limits.MaxAgentResumes {
		return CheckpointFailed, EventCheckpointFailed, "maximum agent resume count exceeded"
	}
	return CheckpointNeedsAgent, EventCheckpointNeedsAgent, reason
}

func (w *IncidentWorkflow) newCheckpointLocked(stageID, trigger string, decision CheckpointDecision, reason, nextStageID string) DecisionCheckpoint {
	results := actionResultsForStage(w.actionResults, w.intent.ID, stageID)
	evidence := make([]string, 0, len(results))
	for _, result := range results {
		if result.EvidenceRef != "" {
			evidence = append(evidence, result.EvidenceRef)
		}
	}
	return DecisionCheckpoint{
		CheckpointID: fmt.Sprintf("%s-checkpoint-%d", w.intent.ID, len(w.checkpoints)+1), StageID: stageID,
		Trigger: trigger, LatestResults: results, NewEvidenceRefs: evidence,
		Decision: decision, DecisionReason: reason, NextStageID: nextStageID, CreatedAt: w.now(),
	}
}

func lookupOutputPath(output map[string]any, path string) (any, bool) {
	var current any = output
	for _, field := range strings.Split(strings.TrimSpace(path), ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[field]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func lookupActionResultPath(result ActionResult, path string) (any, bool) {
	path = strings.TrimSpace(path)
	if path == "operation_status" {
		return result.OperationStatus, result.OperationStatus != ""
	}
	if !strings.HasPrefix(path, "output.") {
		return nil, false
	}
	return lookupOutputPath(result.Output, strings.TrimPrefix(path, "output."))
}

func matchesOutputType(value any, expected ActionOutputType) bool {
	switch expected {
	case ActionOutputString:
		_, ok := value.(string)
		return ok
	case ActionOutputNumber:
		switch value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
			return true
		}
	case ActionOutputInteger:
		switch typed := value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return true
		case float64:
			return typed == float64(int64(typed))
		case json.Number:
			_, err := typed.Int64()
			return err == nil
		}
	case ActionOutputBoolean:
		_, ok := value.(bool)
		return ok
	case ActionOutputObject:
		_, ok := value.(map[string]any)
		return ok
	case ActionOutputArray:
		if reflect.ValueOf(value).IsValid() {
			kind := reflect.TypeOf(value).Kind()
			return kind == reflect.Array || kind == reflect.Slice
		}
	}
	return false
}

func normalizeComparable(value any) any {
	switch typed := value.(type) {
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int8:
		return float64(typed)
	case int16:
		return float64(typed)
	case int32:
		return float64(typed)
	case int64:
		return float64(typed)
	case uint:
		return float64(typed)
	case uint8:
		return float64(typed)
	case uint16:
		return float64(typed)
	case uint32:
		return float64(typed)
	case uint64:
		return float64(typed)
	default:
		return value
	}
}

func findResolvedAction(values []ResolvedAction, intentID, stageID, actionID string) *ResolvedAction {
	for index := range values {
		if values[index].IntentID == intentID && values[index].StageID == stageID && values[index].ActionID == actionID {
			return &values[index]
		}
	}
	return nil
}

func resolvedActionsForStage(values []ResolvedAction, intentID, stageID string) []ResolvedAction {
	result := make([]ResolvedAction, 0)
	for _, value := range values {
		if value.IntentID == intentID && value.StageID == stageID {
			result = append(result, value)
		}
	}
	return result
}

func findActionResult(values []ActionResult, actionID string) *ActionResult {
	for index := range values {
		if values[index].ActionID == actionID {
			return &values[index]
		}
	}
	return nil
}

func actionResultsForStage(values []ActionResult, intentID, stageID string) []ActionResult {
	result := make([]ActionResult, 0)
	for _, value := range values {
		if value.IntentID == intentID && value.StageID == stageID {
			result = append(result, cloneActionResult(value))
		}
	}
	return result
}

func cloneResolvedActions(values []ResolvedAction) []ResolvedAction {
	result := make([]ResolvedAction, len(values))
	for index, value := range values {
		result[index] = value
		result[index].OriginalArguments = cloneAnyMap(value.OriginalArguments)
		result[index].Arguments = cloneAnyMap(value.Arguments)
		result[index].Sources = append([]ResolvedArgumentSource(nil), value.Sources...)
	}
	return result
}

func cloneActionResult(value ActionResult) ActionResult {
	value.Output = cloneAnyMap(value.Output)
	return value
}

func cloneActionResults(values []ActionResult) []ActionResult {
	result := make([]ActionResult, len(values))
	for index := range values {
		result[index] = cloneActionResult(values[index])
	}
	return result
}

func cloneDecisionCheckpoint(value DecisionCheckpoint) DecisionCheckpoint {
	value.LatestResults = cloneActionResults(value.LatestResults)
	value.NewEvidenceRefs = cloneStrings(value.NewEvidenceRefs)
	return value
}

func cloneDecisionCheckpoints(values []DecisionCheckpoint) []DecisionCheckpoint {
	result := make([]DecisionCheckpoint, len(values))
	for index := range values {
		result[index] = cloneDecisionCheckpoint(values[index])
	}
	return result
}
