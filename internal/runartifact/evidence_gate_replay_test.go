package runartifact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestReplayEvidenceGateOnExportedBatch 对已导出的评测批次离线回放证据门禁：
// 模拟每个 Execution Intent 与升级提交时的四维度/已查必引校验，统计命中分布，
// 用于上线前评估误伤。设置 TALON_REPLAY_DIR 指向 evaluation-input 目录后运行。
//
// Intent 门禁只依赖该 Intent 自己的引用，与提交时点无关，可以精确模拟；
// 升级门禁依赖"已获得的维度"，这里用最终调用历史近似（每 Run 至多一次升级
// 且总是最后发生，近似误差可忽略）。
func TestReplayEvidenceGateOnExportedBatch(t *testing.T) {
	dir := os.Getenv("TALON_REPLAY_DIR")
	if dir == "" {
		t.Skip("set TALON_REPLAY_DIR to an exported evaluation-input directory")
	}
	entries, err := filepath.Glob(filepath.Join(dir, "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	intentTotal, intentRejected := 0, 0
	escalationTotal, escalationRejected := 0, 0
	missingDimensions := map[string]int{}
	rejectedRuns := map[string]int{}
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
				rejectedRuns[runLabel]++
				for _, dimension := range missing {
					missingDimensions[dimension]++
				}
			}
		}
		for _, operation := range artifact.Operations {
			if operation.Kind != "escalation" {
				continue
			}
			escalationTotal++
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
				escalationRejected++
				rejectedRuns[runLabel]++
			}
		}
	}

	dimensions := make([]string, 0, len(missingDimensions))
	for dimension := range missingDimensions {
		dimensions = append(dimensions, dimension)
	}
	sort.Strings(dimensions)
	t.Logf("intents: %d total, %d rejected by dimension gate", intentTotal, intentRejected)
	t.Logf("escalations: %d total, %d rejected by unhandled-evidence gate", escalationTotal, escalationRejected)
	for _, dimension := range dimensions {
		t.Logf("  missing %-28s %d", dimension, missingDimensions[dimension])
	}
	labels := make([]string, 0, len(rejectedRuns))
	for label := range rejectedRuns {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for _, label := range labels {
		t.Logf("  run with rejections: %s (%d)", label, rejectedRuns[label])
	}
}
