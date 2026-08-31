// stage_validation.go 收集 ExecutionIntent 冻结前的全部提交期契约校验。
// 这些校验都是无锁纯函数，在任何执行副作用发生之前拒绝非法意图：
// 标识与规模限制、Checkpoint 规则与比较值类型、跨 Action 输出引用，
// 以及 probe→recovery（问题 14/15）与 remediation→probe（问题 18）两条
// fail-closed 链门禁。新增提交期门禁应放在本文件。

package workflow

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var (
	stageIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	outputField     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)
)

// normalizeLimits 把零值限额替换为默认上限。
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

// validateLimits 要求全部限额为正数。
func validateLimits(value ExecutionLimits) error {
	if value.MaxStages <= 0 || value.MaxAgentResumes <= 0 || value.MaxActions <= 0 {
		return fmt.Errorf("workflow execution limits must be positive")
	}
	return nil
}

// validateCheckpointPolicy 校验检查点规则：动作引用、输出路径白名单、
// 比较值类型与决策枚举。
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

// validateCheckpointEquals 要求比较值与已知字段的 JSON 类型一致：
// operation_status/outcome 等字符串字段拒绝布尔字面量（问题 8 的静默不匹配）。
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

// validateDynamicExecutionIntentDraft 是提交期契约校验的入口：标识与规模限制、
// 跨 Action 输出引用的线性前向约束、Checkpoint 规则的目标可达性，
// 并串联 probe/remediation 两条 fail-closed 链门禁。任何拒绝都发生在
// 执行副作用之前，作为可纠正工具结果返回给模型。
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
		if err := validateRemediationStageCheckpoint(stageIndex, stage, stages); err != nil {
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

// validateProbeStageCheckpoint 是 probe Stage 的 fail-closed 门禁（问题 14/15）：
// 必须提供显式 fail-closed 默认决策；任何规则不得对 probe 选择 succeeded。
// 恢复型探测的 healthy 分支可以 continue，但其后必须紧跟 request_recovery；
// 验证型探测（例如验证 fallback）可以在 healthy 后 needs_agent，把结构化结果
// 交回 Agent 决定保持保护并升级等后续动作。两条路径都不能仅凭 probe 关闭事件。
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
	healthyProgress := make(map[string]bool, len(probeActions))
	requiresRecovery := false
	for ruleIndex, rule := range stage.CheckpointPolicy.Rules {
		if rule.Decision == CheckpointSucceeded {
			return fmt.Errorf("intent stages[%d].checkpoint_policy.rules[%d] cannot select succeeded for a probe stage; a healthy probe must either continue to an explicit recovery stage or use needs_agent for a semantic follow-up", stageIndex, ruleIndex)
		}
		if rule.Decision != CheckpointContinue && rule.Decision != CheckpointNeedsAgent {
			continue
		}
		_, isProbe := probeActions[strings.TrimSpace(rule.SourceActionID)]
		outcome, isString := rule.Equals.(string)
		if !isProbe || strings.TrimSpace(rule.OutputPath) != "output.outcome" || !isString || outcome != "healthy" {
			if rule.Decision == CheckpointContinue {
				return fmt.Errorf("intent stages[%d].checkpoint_policy.rules[%d] cannot select %q for a probe stage unless the current probe output.outcome equals healthy", stageIndex, ruleIndex, rule.Decision)
			}
			continue
		}
		healthyProgress[strings.TrimSpace(rule.SourceActionID)] = true
		if rule.Decision == CheckpointContinue {
			requiresRecovery = true
		}
	}
	if requiresRecovery && (stageIndex+1 >= len(stages) || !stageContainsManagedRecovery(stages[stageIndex+1])) {
		return fmt.Errorf("intent stages[%d] continuing after a healthy request_probe requires the next linear stage to contain an explicit request_recovery action", stageIndex)
	}
	for actionID := range probeActions {
		if !healthyProgress[actionID] {
			return fmt.Errorf("intent stages[%d].checkpoint_policy must define a healthy output.outcome rule that either continues to an explicit recovery stage or selects needs_agent for probe action %q", stageIndex, actionID)
		}
	}
	return nil
}

// stageContainsManagedRecovery 判断 Stage 是否包含受管恢复动作。
func stageContainsManagedRecovery(stage ExecutionStageDraft) bool {
	for _, action := range stage.Actions {
		if action.Kind == ActionKindRecovery || strings.TrimSpace(action.ToolName) == "request_recovery" {
			return true
		}
	}
	return false
}

// validateRemediationStageCheckpoint 保证修复动作成功后必须经过业务探测验证：
// 修复操作返回 succeeded 只证明动作执行完成，不证明 Incident 已解决，
// 因此 remediation Stage 不得直接判定 succeeded，成功分支只能 continue 到
// 紧随其后的显式 probe Stage；探测健康后的关闭路径由 probe→recovery 门禁保证。
func validateRemediationStageCheckpoint(stageIndex int, stage ExecutionStageDraft, stages []ExecutionStageDraft) error {
	managed := false
	for _, action := range stage.Actions {
		if action.Kind == ActionKindRemediation {
			managed = true
			break
		}
	}
	if !managed {
		return nil
	}
	for ruleIndex, rule := range stage.CheckpointPolicy.Rules {
		if rule.Decision == CheckpointSucceeded {
			return fmt.Errorf("intent stages[%d].checkpoint_policy.rules[%d] cannot select succeeded for a remediation stage; remediation success must continue to an explicit probe stage", stageIndex, ruleIndex)
		}
	}
	if stage.CheckpointPolicy.DefaultDecision == CheckpointSucceeded {
		return fmt.Errorf("intent stages[%d].checkpoint_policy cannot default to succeeded for a remediation stage; remediation success must continue to an explicit probe stage", stageIndex)
	}
	if stageIndex+1 >= len(stages) || !stageContainsManagedProbe(stages[stageIndex+1]) {
		return fmt.Errorf("intent stages[%d] containing a remediation action requires the next linear stage to contain an explicit request_probe action", stageIndex)
	}
	return nil
}

// stageContainsManagedProbe 判断 Stage 是否包含受管探测动作。
func stageContainsManagedProbe(stage ExecutionStageDraft) bool {
	for _, action := range stage.Actions {
		if action.Kind == ActionKindProbe || strings.TrimSpace(action.ToolName) == "request_probe" {
			return true
		}
	}
	return false
}

// failClosedCheckpointDecision 列出允许作为显式默认决策的保守去向：
// 唤回、失败、升级或阻塞——continue/succeeded 都不是 fail-closed。
func failClosedCheckpointDecision(value CheckpointDecision) bool {
	switch value {
	case CheckpointNeedsAgent, CheckpointFailed, CheckpointEscalate, CheckpointBlocked:
		return true
	default:
		return false
	}
}

// validateActionOutputReference 校验跨 Action 输出引用的来源、路径与类型声明。
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

// validateOutputPath 只允许 operation_status 或 output.<受限字段路径>，
// 不支持数组、通配符或脚本。
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
