package app

import (
	"fmt"
	"io"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/wen/opentalon/internal/agent"
	"github.com/wen/opentalon/internal/runmeta"
	"github.com/wen/opentalon/internal/scenario"
)

// preparedRun contains validated inputs, not live runtime dependencies.
type preparedRun struct {
	config     Config
	scenario   scenario.Scenario
	prompts    agent.PromptSet
	provenance runmeta.Provenance
	runConfig  runmeta.Config
}

func prepareRun(cfg Config) (preparedRun, error) {
	if strings.TrimSpace(cfg.DatasetRoot) == "" {
		return preparedRun{}, fmt.Errorf("dataset root is required")
	}
	if cfg.Storage == nil {
		return preparedRun{}, fmt.Errorf("application storage is required")
	}
	if cfg.Output == nil {
		cfg.Output = io.Discard
	}
	if cfg.ScenarioID == "" {
		cfg.ScenarioID = defaultScenarioID
	}
	if cfg.ClockPollInterval <= 0 {
		cfg.ClockPollInterval = 20 * time.Millisecond
	}
	if cfg.WorkerRetryInterval <= 0 {
		cfg.WorkerRetryInterval = 20 * time.Millisecond
	}

	dataset, err := scenario.LoadDataset(cfg.DatasetRoot)
	if err != nil {
		return preparedRun{}, fmt.Errorf("load scenario dataset: %w", err)
	}
	item, ok := dataset.Find(strings.TrimSpace(cfg.ScenarioID))
	if !ok {
		return preparedRun{}, fmt.Errorf("scenario %q was not found", cfg.ScenarioID)
	}
	prompts, err := agent.LoadPromptSet(cfg.PromptDirectory)
	if err != nil {
		return preparedRun{}, fmt.Errorf("load Agent prompts: %w", err)
	}
	provenance := cfg.Provenance
	if strings.TrimSpace(provenance.DatasetVersion) == "" {
		provenance.DatasetVersion = dataset.Version
	}
	if strings.TrimSpace(provenance.PromptVersion) == "" {
		provenance.PromptVersion = prompts.Version
	}
	if strings.TrimSpace(provenance.PromptDigest) == "" {
		provenance.PromptDigest = prompts.Digest
	}
	provenance = normalizeProvenance(provenance, cfg.DatasetRoot)
	runConfig := cfg.RunConfig
	runConfig.AgentMaxSteps = cfg.AgentMaxSteps
	if runConfig.AgentMaxSteps == 0 {
		runConfig.AgentMaxSteps = agent.DefaultMaxSteps
	}
	if runConfig.MaxModelCalls == 0 {
		runConfig.MaxModelCalls = agent.DefaultMaxModelCalls
	}
	runConfig.AutoApprove = cfg.AutoApprove
	return preparedRun{config: cfg, scenario: item.Scenario, prompts: prompts, provenance: provenance, runConfig: runConfig}, nil
}

func normalizeProvenance(value runmeta.Provenance, datasetRoot string) runmeta.Provenance {
	value.CodeVersion = strings.TrimSpace(value.CodeVersion)
	if value.CodeVersion == "" {
		value.CodeVersion = detectedCodeVersion()
	}
	value.DatasetVersion = strings.TrimSpace(value.DatasetVersion)
	if value.DatasetVersion == "" {
		value.DatasetVersion = filepath.Base(filepath.Clean(datasetRoot))
	}
	if value.DatasetVersion == "." || value.DatasetVersion == string(filepath.Separator) {
		value.DatasetVersion = "unknown"
	}
	value.PromptVersion = strings.TrimSpace(value.PromptVersion)
	value.PromptDigest = strings.TrimSpace(value.PromptDigest)
	return value
}

func detectedCodeVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	revision := ""
	modified := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = strings.TrimSpace(setting.Value)
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if revision == "" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		revision = info.Main.Version
	}
	if revision == "" {
		return "unknown"
	}
	if modified {
		return revision + "+dirty"
	}
	return revision
}
