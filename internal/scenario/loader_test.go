package scenario

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadDatasetLoadsVersionedScenarioPairs(t *testing.T) {
	dataset, err := LoadDataset(testDatasetRoot(t))
	require.NoError(t, err)
	require.Len(t, dataset.Cases, 3)

	ids := make([]string, 0, len(dataset.Cases))
	for _, item := range dataset.Cases {
		ids = append(ids, item.Scenario.Metadata.ID)
		require.Equal(t, item.Scenario.Metadata.ID, item.Expectations.ScenarioID)
		require.NotEmpty(t, item.Directory)
	}
	require.Equal(t, []string{
		"connection-recovery-two-cycles-001",
		"credential-revoked-escalation-001",
		"mapping-regression-rollback-001",
	}, ids)

	item, ok := dataset.Find("mapping-regression-rollback-001")
	require.True(t, ok)
	require.Equal(t, "config_regression", item.Scenario.Metadata.Category)
	require.Equal(t, "error_rate_degradation", item.Expectations.Controller["incident_type"])
	require.Equal(t, "rollback_mapping", item.Scenario.RemediationTools[0].Name)

	_, ok = dataset.Find("missing-scenario")
	require.False(t, ok)
}

func TestLoadDatasetRejectsMismatchedScenarioID(t *testing.T) {
	root := newDatasetWithCase(t, "mismatch", validScenarioFiles(t))
	expectationsPath := filepath.Join(root, scenariosDirectoryName, "mismatch", expectationsFileName)
	expectations := readFile(t, expectationsPath)
	expectations = strings.ReplaceAll(expectations, "mapping-regression-rollback-001", "different-id")
	require.NoError(t, os.WriteFile(expectationsPath, []byte(expectations), 0o600))

	_, err := LoadDataset(root)
	require.ErrorContains(t, err, "scenario ID mismatch")
}

func TestLoadDatasetRejectsMissingExpectations(t *testing.T) {
	files := validScenarioFiles(t)
	delete(files, expectationsFileName)
	root := newDatasetWithCase(t, "missing-expectations", files)

	_, err := LoadDataset(root)
	require.ErrorContains(t, err, "expectations.yaml")
}

func TestLoadDatasetRejectsUnsupportedSchema(t *testing.T) {
	files := validScenarioFiles(t)
	files[scenarioFileName] = strings.Replace(
		files[scenarioFileName],
		ScenarioSchemaV1_1,
		"toolops-scenario/v2",
		1,
	)
	root := newDatasetWithCase(t, "unsupported-schema", files)

	_, err := LoadDataset(root)
	require.ErrorContains(t, err, "unsupported scenario schema")
}

func TestLoadDatasetRejectsDuplicateScenarioIDs(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"first", "second"} {
		writeCase(t, root, directory, validScenarioFiles(t))
	}

	_, err := LoadDataset(root)
	require.ErrorContains(t, err, "duplicate scenario ID")
}

func TestLoadDatasetRejectsSymlinkedScenarioDirectory(t *testing.T) {
	root := t.TempDir()
	scenariosRoot := filepath.Join(root, scenariosDirectoryName)
	require.NoError(t, os.MkdirAll(scenariosRoot, 0o700))
	target := filepath.Join(testDatasetRoot(t), scenariosDirectoryName, "mapping-regression-rollback")
	require.NoError(t, os.Symlink(target, filepath.Join(scenariosRoot, "linked-case")))

	_, err := LoadDataset(root)
	require.ErrorContains(t, err, "must not be a symlink")
}

func validScenarioFiles(t *testing.T) map[string]string {
	t.Helper()
	directory := filepath.Join(
		testDatasetRoot(t),
		scenariosDirectoryName,
		"mapping-regression-rollback",
	)
	return map[string]string{
		scenarioFileName:     readFile(t, filepath.Join(directory, scenarioFileName)),
		expectationsFileName: readFile(t, filepath.Join(directory, expectationsFileName)),
	}
}

func newDatasetWithCase(t *testing.T, directory string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	writeCase(t, root, directory, files)
	return root
}

func writeCase(t *testing.T, root, directory string, files map[string]string) {
	t.Helper()
	caseDirectory := filepath.Join(root, scenariosDirectoryName, directory)
	require.NoError(t, os.MkdirAll(caseDirectory, 0o700))
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(caseDirectory, name), []byte(content), 0o600))
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func testDatasetRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", "data", "toolops-v1"))
}
