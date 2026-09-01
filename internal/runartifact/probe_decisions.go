package runartifact

import (
	"fmt"
	"strings"

	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/workflow"
)

// probe 终局决策门禁（问题 22）：复合故障第一周期的失败探测是第二周期的
// 必要新证据输入，不是终审判决。当能力目录仍存在"已授权、既往未尝试、也不在
// 本次草案内"的修复动作时，probe Stage 的 fail-closed 去向不得选择终局决策
// （failed/escalate/blocked），只能 needs_agent 唤回 Agent 结合新证据重新评估；
// 确无剩余能力时才允许终局。判定材料与升级门禁 Gate B 同源：既往尝试以
// ResolvedActions 为准（含执行失败与被平台拒绝的动作），授权面以能力目录为准。

// RemainingAuthorizedRemediations 返回 authorizedTools 中排除既往 Intent 已尝试
// 动作与本次草案自带动作后，仍可自治尝试的授权修复动作名。输出保持传入顺序、
// 去重且非空才收录，便于拼进稳定的拒绝消息。
func RemainingAuthorizedRemediations(resolved []workflow.ResolvedAction, stages []workflow.ExecutionStageDraft, authorizedTools []string) []string {
	planned := make(map[string]struct{})
	for _, action := range resolved {
		if action.Kind == workflow.ActionKindRemediation {
			planned[action.ToolName] = struct{}{}
		}
	}
	for _, stage := range stages {
		for _, action := range stage.Actions {
			if action.Kind == workflow.ActionKindRemediation {
				planned[action.ToolName] = struct{}{}
			}
		}
	}
	remaining := make([]string, 0, len(authorizedTools))
	seen := make(map[string]struct{}, len(authorizedTools))
	for _, name := range authorizedTools {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		if _, attempted := planned[name]; attempted {
			continue
		}
		seen[name] = struct{}{}
		remaining = append(remaining, name)
	}
	return remaining
}

// terminalCheckpointDecision 判定检查点决策是否终局：failed（运行失败）、
// escalate（升级人工）与 blocked（授权阻塞）都会结束自治循环；只有
// needs_agent 会把控制权交回 Agent。
func terminalCheckpointDecision(decision workflow.CheckpointDecision) bool {
	switch decision {
	case workflow.CheckpointFailed, workflow.CheckpointEscalate, workflow.CheckpointBlocked:
		return true
	default:
		return false
	}
}

// stageContainsProbeAction 与 Workflow 提交校验同口径识别 probe Stage：
// 显式 kind=probe 或受管工具名 request_probe。
func stageContainsProbeAction(stage workflow.ExecutionStageDraft) bool {
	for _, action := range stage.Actions {
		if action.Kind == workflow.ActionKindProbe || strings.TrimSpace(action.ToolName) == "request_probe" {
			return true
		}
	}
	return false
}

// ValidateIntentProbeDecisions 是 submit_execution_intent 的 probe 终局决策
// 门禁（问题 22）：既往尝试以 Checkpoint 实时同步的 ResolvedActions 为准，
// 与升级门禁 Gate B 使用同一判定基础。被拒的提交作为可纠正工具结果返回，
// 模型把默认决策改为 needs_agent 后即可重新提交。
func (r *Recorder) ValidateIntentProbeDecisions(stages []workflow.ExecutionStageDraft, authorizedTools []string, probePolicy platform.ProbeEscalationPolicy) error {
	if r == nil {
		return fmt.Errorf("run artifact recorder is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return GateProbeCheckpointDecisions(r.artifact.ResolvedActions, stages, authorizedTools, probePolicy)
}

// GateProbeCheckpointDecisions 是 submit_execution_intent 的 probe 终局决策门禁：
// 存在剩余授权修复动作时，probe Stage 的默认决策与规则都不得选择终局决策。
// 场景声明 escalation_probe_policy=required 时同样禁止：探测适用且会改变决策
// （如瞬时故障自愈、状态随时间变化），探测结果必须作为新证据交回 Agent。
// 该函数不依赖 Recorder 内部状态，便于对历史 Artifact 离线回放门禁效果。
func GateProbeCheckpointDecisions(resolved []workflow.ResolvedAction, stages []workflow.ExecutionStageDraft, authorizedTools []string, probePolicy platform.ProbeEscalationPolicy) error {
	remaining := RemainingAuthorizedRemediations(resolved, stages, authorizedTools)
	probeRequired := probePolicy == platform.ProbeEscalationRequired
	if len(remaining) == 0 && !probeRequired {
		return nil
	}
	for stageIndex, stage := range stages {
		if !stageContainsProbeAction(stage) {
			continue
		}
		if terminalCheckpointDecision(stage.CheckpointPolicy.DefaultDecision) {
			if len(remaining) > 0 {
				return fmt.Errorf("intent stages[%d]（%s）的 probe 默认决策 %q 被拒：能力目录仍存在未尝试的授权修复动作（%s）。探测失败是新证据输入，不是终审判决——存在可继续的自治路径时必须用 needs_agent 唤回自己结合新证据重新评估，不得用 failed/escalate/blocked 终止；若新证据表明剩余动作不适用，可在 needs_agent 评估后通过 escalate_incident 升级人工",
					stageIndex, strings.TrimSpace(stage.StageID), stage.CheckpointPolicy.DefaultDecision, strings.Join(remaining, "、"))
			}
			return fmt.Errorf("intent stages[%d]（%s）的 probe 默认决策 %q 被拒：当前场景的探测适用性为 required——探测结果是会改变决策的新证据（瞬时故障可能已自愈、状态可能已变化），必须用 needs_agent 唤回自己结合新证据重新评估后重新探测或升级；不得用 failed/escalate/blocked 在首次探测不健康时终局",
				stageIndex, strings.TrimSpace(stage.StageID), stage.CheckpointPolicy.DefaultDecision)
		}
		for ruleIndex, rule := range stage.CheckpointPolicy.Rules {
			if !terminalCheckpointDecision(rule.Decision) {
				continue
			}
			if len(remaining) > 0 {
				return fmt.Errorf("intent stages[%d]（%s）checkpoint_policy.rules[%d] 对 probe 选择 %q 被拒：能力目录仍存在未尝试的授权修复动作（%s）。探测失败必须以 needs_agent 交回 Agent 结合新证据重新评估，不得用终局规则在探测不健康时提前终止；若新证据表明剩余动作不适用，可在 needs_agent 评估后通过 escalate_incident 升级人工",
					stageIndex, strings.TrimSpace(stage.StageID), ruleIndex, rule.Decision, strings.Join(remaining, "、"))
			}
			return fmt.Errorf("intent stages[%d]（%s）checkpoint_policy.rules[%d] 对 probe 选择 %q 被拒：当前场景的探测适用性为 required——探测不健康是新证据输入而非终局结论，规则只允许 continue 或 needs_agent，终局判断须在 needs_agent 评估后通过 escalate_incident 完成",
				stageIndex, strings.TrimSpace(stage.StageID), ruleIndex, rule.Decision)
		}
	}
	return nil
}
