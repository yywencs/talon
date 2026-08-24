package runartifact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/workflow"
)

// TestReplayProbeDecisionGateOnExportedBatch 对已导出的评测批次离线回放 probe
// 终局决策门禁（问题 22）：按 Intent 提交顺序重放——每个 Intent 的"既往尝试"
// 只包含更早 Intent 的 ResolvedActions，还原提交时点模型与门禁看到的真实状态。
// 用于上线前评估误伤。设置 TALON_REPLAY_DIR 指向 evaluation-input 目录后运行；
// 可选 TALON_REPLAY_DATASET（默认 data/toolops-v2）提供各场景的授权修复动作清单。
func TestReplayProbeDecisionGateOnExportedBatch(t *testing.T) {
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

	intentTotal, rejected := 0, 0
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

		earlierIntentIDs := map[string]struct{}{}
		for _, intent := range artifact.ExecutionIntents {
			intentTotal++
			attempted := make([]workflow.ResolvedAction, 0, len(artifact.ResolvedActions))
			for _, resolved := range artifact.ResolvedActions {
				if _, earlier := earlierIntentIDs[resolved.IntentID]; earlier {
					attempted = append(attempted, resolved)
				}
			}
			drafts := make([]workflow.ExecutionStageDraft, 0, len(intent.Stages))
			for _, stage := range intent.Stages {
				drafts = append(drafts, workflow.ExecutionStageDraft{StageID: stage.StageID, Goal: stage.Goal,
					Actions: stage.Actions, SuccessCriteria: stage.SuccessCriteria, CheckpointPolicy: stage.CheckpointPolicy})
			}
			if gateErr := GateProbeCheckpointDecisions(attempted, drafts, authorizedByScenario[artifact.ScenarioID]); gateErr != nil {
				rejected++
				rejectedRuns[runLabel] = append(rejectedRuns[runLabel], "probe-decision")
			}
			earlierIntentIDs[intent.ID] = struct{}{}
		}
	}
	t.Logf("intents with probe decision gating: %d total, %d rejected", intentTotal, rejected)
	labels := make([]string, 0, len(rejectedRuns))
	for label := range rejectedRuns {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for _, label := range labels {
		t.Logf("  run with rejections: %s %v", label, rejectedRuns[label])
	}
}
