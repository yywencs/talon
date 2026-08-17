package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadPromptSetUsesEmbeddedDefaults(t *testing.T) {
	prompts, err := LoadPromptSet("")
	require.NoError(t, err)
	assert.Equal(t, "toolops-agent/v3", prompts.Version)
	assert.Len(t, prompts.Digest, 64)
	assert.Contains(t, prompts.SystemTemplate, incidentIDPlaceholder)
	assert.Contains(t, prompts.SystemTemplate, "把根因拆分为可独立验证的原子事实")
	assert.Contains(t, prompts.SystemTemplate, "对比、因果或复合结论必须覆盖所有组成事实和对比侧")
	assert.Contains(t, prompts.systemPrompt("incident-1", ""), `Incident "incident-1"`)
}

func TestLoadPromptSetSupportsRuntimeOverrideAndContentDigest(t *testing.T) {
	directory := t.TempDir()
	writePromptFile(t, directory, systemPromptFile, "处理 {{incident_id}}，并引用直接证据。")
	writePromptFile(t, directory, defaultInstructionFile, "开始调查。")
	writePromptFile(t, directory, promptManifestFile, `{"id":"custom/v3"}`)

	first, err := LoadPromptSet(directory)
	require.NoError(t, err)
	assert.Equal(t, "custom/v3", first.Version)
	assert.Equal(t, `处理 "incident-2"，并引用直接证据。`, first.systemPrompt("incident-2", ""))

	writePromptFile(t, directory, defaultInstructionFile, "重新开始调查。")
	second, err := LoadPromptSet(directory)
	require.NoError(t, err)
	assert.Equal(t, first.Version, second.Version)
	assert.NotEqual(t, first.Digest, second.Digest)
}

func TestLoadPromptSetRejectsIncompleteDirectory(t *testing.T) {
	directory := t.TempDir()
	writePromptFile(t, directory, systemPromptFile, "missing placeholder")
	writePromptFile(t, directory, defaultInstructionFile, "开始调查。")
	writePromptFile(t, directory, promptManifestFile, `{"id":"custom/v1"}`)

	_, err := LoadPromptSet(directory)
	assert.ErrorContains(t, err, incidentIDPlaceholder)
}

func writePromptFile(t *testing.T, directory, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600))
}
