// Package evaluation builds versioned inputs for the offline Talon Evaluator.
package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wen/opentalon/internal/runartifact"
	"github.com/wen/opentalon/internal/scenario"
)

const (
	InputSchemaVersion  = "talon.evaluation-input/v1"
	ExportSchemaVersion = "talon.evaluation-export/v3"
	manifestFileName    = "manifest.json"
)

var ErrNoArtifacts = errors.New("no terminal run artifacts match the selected versions")

type Input struct {
	SchemaVersion string                  `json:"schema_version"`
	Artifact      runartifact.RunArtifact `json:"artifact"`
	Expectations  scenario.Expectations   `json:"expectations"`
}

type ManifestRun struct {
	RunID      string `json:"run_id"`
	ScenarioID string `json:"scenario_id"`
	Outcome    string `json:"outcome"`
	File       string `json:"file"`
}

type Manifest struct {
	SchemaVersion         string        `json:"schema_version"`
	ArtifactSchemaVersion string        `json:"artifact_schema_version"`
	CodeVersion           string        `json:"code_version"`
	DatasetVersion        string        `json:"dataset_version"`
	Runs                  []ManifestRun `json:"runs"`
}

type ExportConfig struct {
	Store          runartifact.Store
	DataRoot       string
	DatasetVersion string
	CodeVersion    string
	OutputDir      string
}

func ExportBatch(ctx context.Context, config ExportConfig) (Manifest, error) {
	if config.Store == nil {
		return Manifest{}, fmt.Errorf("run artifact store is required")
	}
	datasetVersion, err := cleanVersion(config.DatasetVersion, "dataset version")
	if err != nil {
		return Manifest{}, err
	}
	codeVersion := strings.TrimSpace(config.CodeVersion)
	if codeVersion == "" {
		return Manifest{}, fmt.Errorf("code version is required")
	}
	dataRoot := strings.TrimSpace(config.DataRoot)
	if dataRoot == "" {
		return Manifest{}, fmt.Errorf("data root is required")
	}
	outputDir := strings.TrimSpace(config.OutputDir)
	if outputDir == "" {
		return Manifest{}, fmt.Errorf("output directory is required")
	}

	dataset, err := scenario.LoadDataset(filepath.Join(dataRoot, datasetVersion))
	if err != nil {
		return Manifest{}, fmt.Errorf("load dataset version %q: %w", datasetVersion, err)
	}
	artifacts := make([]runartifact.RunArtifact, 0)
	for _, outcome := range []string{"completed", "failed"} {
		selected, err := config.Store.List(ctx, runartifact.VersionFilter{
			SchemaVersion: runartifact.SchemaVersion, CodeVersion: codeVersion,
			DatasetVersion: datasetVersion, Outcome: outcome,
		})
		if err != nil {
			return Manifest{}, fmt.Errorf("select %s run artifacts: %w", outcome, err)
		}
		artifacts = append(artifacts, selected...)
	}
	if len(artifacts) == 0 {
		return Manifest{}, ErrNoArtifacts
	}
	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].StartedAt.Equal(artifacts[j].StartedAt) {
			return artifacts[i].RunID < artifacts[j].RunID
		}
		return artifacts[i].StartedAt.Before(artifacts[j].StartedAt)
	})

	manifest := Manifest{
		SchemaVersion: ExportSchemaVersion, ArtifactSchemaVersion: runartifact.SchemaVersion,
		CodeVersion: codeVersion, DatasetVersion: datasetVersion, Runs: make([]ManifestRun, 0, len(artifacts)),
	}
	inputs := make([]Input, 0, len(artifacts))
	for _, artifact := range artifacts {
		if err := validateArtifact(artifact, codeVersion, datasetVersion); err != nil {
			return Manifest{}, err
		}
		item, ok := dataset.Find(artifact.ScenarioID)
		if !ok {
			return Manifest{}, fmt.Errorf("scenario %q from run %q is absent from dataset %q", artifact.ScenarioID, artifact.RunID, datasetVersion)
		}
		fileName := artifact.RunID + ".json"
		manifest.Runs = append(manifest.Runs, ManifestRun{
			RunID: artifact.RunID, ScenarioID: artifact.ScenarioID, Outcome: artifact.Outcome, File: fileName,
		})
		inputs = append(inputs, Input{SchemaVersion: InputSchemaVersion, Artifact: artifact, Expectations: item.Expectations})
	}
	if err := writeBatch(outputDir, manifest, inputs); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func cleanVersion(value, label string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	if value == "." || value == ".." || filepath.Base(value) != value || strings.ContainsAny(value, `/\\`) {
		return "", fmt.Errorf("%s must be a directory name, got %q", label, value)
	}
	return value, nil
}

func validateArtifact(artifact runartifact.RunArtifact, codeVersion, datasetVersion string) error {
	if artifact.SchemaVersion != runartifact.SchemaVersion || !terminalOutcome(artifact.Outcome) ||
		artifact.Provenance.CodeVersion != codeVersion || artifact.Provenance.DatasetVersion != datasetVersion {
		return fmt.Errorf("run artifact %q does not match the selected versions", artifact.RunID)
	}
	if artifact.RunID == "" || filepath.Base(artifact.RunID) != artifact.RunID || strings.ContainsAny(artifact.RunID, `/\\`) {
		return fmt.Errorf("run artifact has unsafe run ID %q", artifact.RunID)
	}
	return nil
}

func terminalOutcome(value string) bool {
	return value == "completed" || value == "failed"
}

func writeBatch(outputDir string, manifest Manifest, inputs []Input) (err error) {
	absoluteOutput, err := filepath.Abs(outputDir)
	if err != nil {
		return fmt.Errorf("resolve output directory: %w", err)
	}
	if _, err := os.Stat(absoluteOutput); err == nil {
		return fmt.Errorf("output directory %q already exists", absoluteOutput)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect output directory %q: %w", absoluteOutput, err)
	}
	parent := filepath.Dir(absoluteOutput)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create output parent directory: %w", err)
	}
	temporary, err := os.MkdirTemp(parent, ".talon-export-")
	if err != nil {
		return fmt.Errorf("create temporary export directory: %w", err)
	}
	defer func() {
		if cleanupErr := os.RemoveAll(temporary); err == nil && cleanupErr != nil {
			err = fmt.Errorf("clean temporary export directory: %w", cleanupErr)
		}
	}()
	for index, input := range inputs {
		if err := writeJSON(filepath.Join(temporary, manifest.Runs[index].File), input); err != nil {
			return err
		}
	}
	if err := writeJSON(filepath.Join(temporary, manifestFileName), manifest); err != nil {
		return err
	}
	if err := os.Rename(temporary, absoluteOutput); err != nil {
		return fmt.Errorf("publish export directory: %w", err)
	}
	return nil
}

func writeJSON(path string, value any) error {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode export JSON: %w", err)
	}
	payload = append(payload, '\n')
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return fmt.Errorf("write export JSON %q: %w", path, err)
	}
	return nil
}
