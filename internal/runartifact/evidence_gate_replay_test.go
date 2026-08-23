package runartifact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/workflow"
)

// TestReplayEvidenceGateOnExportedBatch 对已导出的评测批次离线回放证据门禁：
// 模拟每个 Execution Intent 与升级提交时的四维度/已查必引/先探测/如实申报预算
// 校验，统计命中分布，用于上线前评估误伤。设置 TALON_REPLAY_DIR 指向
// evaluation-input 目录后运行；可选 TALON_REPLAY_DATASET（默认 data/toolops-v2）
// 提供各场景的授权修复动作清单。
//
// Intent 门禁只依赖该 Intent 自己的引用，与提交时点无关，可以精确模拟；
// 升级门禁依赖"已获得的维度"与操作历史，这里用最终状态近似（每 Run 至多一次
// 升级且总是最后发生，近似误差可忽略）。
func TestReplayEvidenceGateOnExportedBatch(t *testing.T) {
	dir := os.Getenv("TALON_REPLAY_DIR")
	if dir == "" {
		t.Skip("set TALON_REPLAY_DIR to an exported evaluation-input directory")
	}
	datasetRoot := os.Getenv("TALON_REPLAY_DATASET")
	if datasetRoot == "" {
		datasetRoot = "data/toolops-v2"
	}
	authorizedByScenario := scenarioAuthorizedTools(t, datasetRoot)
	entries, err := filepath.Glob(filepath.Join(dir, "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	intentTotal, intentRejected := 0, 0
	escalationTotal, dimensionRejected, probeRejected, budgetRejected := 0, 0, 0, 0
	missingDimensions := map[string]int{}
	rejectedRuns := map[string][]string{}
	for _, path := range entries {
		if strings.HasSuffix(path, "manifest.json") {
			continue
		}
		payload, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		var input struct {
			Artifact RunArtifact `json:"artifact"`
		}
		require.NoError(t, json.Unmarshal(payload, &input))
		artifact := input.Artifact
		runLabel := artifact.ScenarioID + " " + artifact.RunID

		for _, intent := range artifact.ExecutionIntents {
			intentTotal++
			_, cited := EvidenceDimensionCoverage(artifact.AgentRuns, intent.EvidenceRefs)
			missing := make([]string, 0, len(RequiredEvidenceDimensions))
			for _, dimension := range RequiredEvidenceDimensions {
				if _, covered := cited[dimension]; !covered {
					missing = append(missing, dimension)
				}
			}
			if len(missing) > 0 {
				intentRejected++
				rejectedRuns[runLabel] = append(rejectedRuns[runLabel], "intent-dimension")
				for _, dimension := range missing {
					missingDimensions[dimension]++
				}
			}
		}
		authorizedTools := authorizedByScenario[artifact.ScenarioID]
		for _, operation := range artifact.Operations {
			if operation.Kind != "escalation" {
				continue
			}
			escalationTotal++
			reasonCode := platform.EscalationReasonCode(asString(operation.Result["reason_code"]))
			refs, _ := operation.Result["evidence_refs"].([]any)
			refStrings := make([]string, 0, len(refs))
			for _, ref := range refs {
				if value, ok := ref.(string); ok {
					refStrings = append(refStrings, value)
				}
			}
			consulted, cited := EvidenceDimensionCoverage(artifact.AgentRuns, refStrings)
			unhandled := false
			for _, dimension := range RequiredEvidenceDimensions {
				if _, obtained := consulted[dimension]; !obtained {
					continue
				}
				if _, covered := cited[dimension]; !covered {
					unhandled = true
					missingDimensions["escalation:"+dimension]++
				}
			}
			if unhandled {
				dimensionRejected++
				rejectedRuns[runLabel] = append(rejectedRuns[runLabel], "escalation-dimension")
			}
			// 与运行时门禁同口径：以 ResolvedActions 判定尝试过（含被拒尝试）。
			probeAttempted, attempted := false, map[string]struct{}{}
			for _, resolved := range artifact.ResolvedActions {
				switch resolved.Kind {
				case workflow.ActionKindProbe:
					probeAttempted = true
				case workflow.ActionKindRemediation:
					attempted[resolved.ToolName] = struct{}{}
				}
			}
			if (reasonCode == platform.EscalationReasonNoSafeRemediationAvailable ||
				reasonCode == platform.EscalationReasonCredentialChangeRequiresHuman) && !probeAttempted {
				probeRejected++
				rejectedRuns[runLabel] = append(rejectedRuns[runLabel], "gate-probe")
			}
			if len(authorizedTools) > 0 && reasonCode != platform.EscalationReasonWorkflowBudgetExhausted {
				exhausted := true
				for _, name := range authorizedTools {
					if _, ok := attempted[name]; !ok {
						exhausted = false
						break
					}
				}
				if exhausted {
					budgetRejected++
					rejectedRuns[runLabel] = append(rejectedRuns[runLabel], "gate-budget")
				}
			}
		}
	}

	dimensions := make([]string, 0, len(missingDimensions))
	for dimension := range missingDimensions {
		dimensions = append(dimensions, dimension)
	}
	sort.Strings(dimensions)
	t.Logf("intents: %d total, %d rejected by dimension gate", intentTotal, intentRejected)
	t.Logf("escalations: %d total, dimension-rejected %d, gate-probe %d, gate-budget %d", escalationTotal, dimensionRejected, probeRejected, budgetRejected)
	for _, dimension := range dimensions {
		t.Logf("  missing %-28s %d", dimension, missingDimensions[dimension])
	}
	labels := make([]string, 0, len(rejectedRuns))
	for label := range rejectedRuns {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for _, label := range labels {
		t.Logf("  run with rejections: %s %v", label, rejectedRuns[label])
	}
}

func asString(value any) string {
	text, _ := value.(string)
	return text
}

// scenarioAuthorizedTools 从数据集各场景定义解析 Agent 有权调用的修复动作名。
func scenarioAuthorizedTools(t *testing.T, datasetRoot string) map[string][]string {
	t.Helper()
	result := make(map[string][]string)
	entries, err := filepath.Glob(filepath.Join(datasetRoot, "scenarios", "*", "scenario.yaml"))
	require.NoError(t, err)
	for _, path := range entries {
		payload, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		scenarioDir := filepath.Base(filepath.Dir(path))
		sections := strings.SplitN(string(payload), "remediation_tools:", 2)
		if len(sections) < 2 {
			continue
		}
		section := sections[1]
		if index := strings.Index(section, "\n\n"); index >= 0 {
			section = section[:index]
		}
		tools := []string{}
		for _, line := range strings.Split(section, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "- name: ") {
				tools = append(tools, strings.TrimSpace(strings.TrimPrefix(trimmed, "- name: ")))
			}
			// 无权动作不计入授权清单（回放 Gate B 的判定口径与运行时一致）。
			if trimmed == "agent_authorized: false" && len(tools) > 0 {
				tools = tools[:len(tools)-1]
			}
		}
		if len(tools) > 0 {
			result[scenarioDir+"-001"] = tools
		}
	}
	return result
}
