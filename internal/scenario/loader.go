package scenario

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
)

const (
	scenariosDirectoryName = "scenarios"
	datasetFileName        = "dataset.yaml"
	scenarioFileName       = "scenario.yaml"
	expectationsFileName   = "expectations.yaml"
)

// LoadDataset 查找 root 下的全部场景目录，加载并校验每组 YAML，
// 最后按照目录名称的稳定顺序返回场景数据。
func LoadDataset(root string) (*Dataset, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve dataset root %q: %w", root, err)
	}

	metadata, err := loadYAML[DatasetMetadata](filepath.Join(absoluteRoot, datasetFileName))
	if err != nil {
		return nil, err
	}
	metadata.Version = strings.TrimSpace(metadata.Version)
	if metadata.Version == "" {
		return nil, fmt.Errorf("dataset version in %q is required", filepath.Join(absoluteRoot, datasetFileName))
	}

	scenariosRoot := filepath.Join(absoluteRoot, scenariosDirectoryName)
	entries, err := os.ReadDir(scenariosRoot)
	if err != nil {
		return nil, fmt.Errorf("read scenarios directory %q: %w", scenariosRoot, err)
	}

	dataset := &Dataset{Root: absoluteRoot, Version: metadata.Version}
	seenIDs := make(map[string]string)
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("scenario directory %q must not be a symlink", entry.Name())
		}
		if !entry.IsDir() {
			continue
		}

		directory := filepath.Join(scenariosRoot, entry.Name())
		loaded, err := loadCase(directory)
		if err != nil {
			return nil, err
		}
		id := loaded.Scenario.Metadata.ID
		if previous, exists := seenIDs[id]; exists {
			return nil, fmt.Errorf("duplicate scenario ID %q in %q and %q", id, previous, directory)
		}
		seenIDs[id] = directory
		dataset.Cases = append(dataset.Cases, loaded)
	}

	if len(dataset.Cases) == 0 {
		return nil, fmt.Errorf("dataset %q contains no scenario directories", absoluteRoot)
	}
	return dataset, nil
}

func loadCase(directory string) (Case, error) {
	scenarioPath := filepath.Join(directory, scenarioFileName)
	expectationsPath := filepath.Join(directory, expectationsFileName)

	scenarioDocument, err := loadYAML[Scenario](scenarioPath)
	if err != nil {
		return Case{}, err
	}
	if err := validateScenario(scenarioDocument); err != nil {
		return Case{}, fmt.Errorf("validate %q: %w", scenarioPath, err)
	}

	expectationsDocument, err := loadYAML[Expectations](expectationsPath)
	if err != nil {
		return Case{}, err
	}
	if err := validateExpectations(expectationsDocument); err != nil {
		return Case{}, fmt.Errorf("validate %q: %w", expectationsPath, err)
	}
	if expectationsDocument.ScenarioID != scenarioDocument.Metadata.ID {
		return Case{}, fmt.Errorf(
			"scenario ID mismatch in %q: scenario=%q expectations=%q",
			directory,
			scenarioDocument.Metadata.ID,
			expectationsDocument.ScenarioID,
		)
	}

	return Case{
		Directory:    directory,
		Scenario:     scenarioDocument,
		Expectations: expectationsDocument,
	}, nil
}

func loadYAML[T any](path string) (T, error) {
	var document T
	data, err := os.ReadFile(path)
	if err != nil {
		return document, fmt.Errorf("read YAML %q: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		return document, fmt.Errorf("parse YAML %q: %w", path, err)
	}
	return document, nil
}
