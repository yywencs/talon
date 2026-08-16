package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadDirectoryLoadsProjectSkills(t *testing.T) {
	registry, err := LoadDirectory("../../skills")
	require.NoError(t, err)
	require.Equal(t, 3, registry.Len())

	catalog := registry.Catalog()
	require.Equal(t, []string{
		"connection-diagnosis", "credential-diagnosis", "mapping-diagnosis",
	}, []string{catalog[0].Name, catalog[1].Name, catalog[2].Name})
	for _, entry := range catalog {
		assert.Contains(t, entry.Description, "Use when")
		assert.Contains(t, entry.Description, "Don't use when")
	}

	connection, found := registry.Get("connection-diagnosis")
	require.True(t, found)
	assert.Contains(t, connection.Instructions, "expected_pool_generation")
	assert.Equal(t, []string{"query_logs", "query_traces", "get_connection_metadata"}, connection.AllowedTools)
	assert.True(t, strings.HasPrefix(connection.Digest, "sha256:"))
	assert.Len(t, connection.Digest, len("sha256:")+64)
}

func TestRegistryReturnsDefensiveCopies(t *testing.T) {
	registry, err := LoadDirectory("../../skills")
	require.NoError(t, err)

	first, found := registry.Get("mapping-diagnosis")
	require.True(t, found)
	first.AllowedTools[0] = "changed"
	second, found := registry.Get("mapping-diagnosis")
	require.True(t, found)
	assert.NotEqual(t, "changed", second.AllowedTools[0])
	_, found = registry.Get("unknown")
	assert.False(t, found)
}

func TestLoadDirectoryValidatesSkillPackages(t *testing.T) {
	tests := []struct {
		name      string
		directory string
		skill     string
		policy    string
		wantError string
	}{
		{
			name: "missing frontmatter", directory: "broken-skill",
			skill: "# Broken", wantError: "must start with YAML frontmatter",
		},
		{
			name: "name mismatch", directory: "expected-name",
			skill:     skillDocument("different-name", "description", "instructions"),
			wantError: "does not match directory",
		},
		{
			name: "empty instructions", directory: "empty-skill",
			skill:     skillDocument("empty-skill", "description", ""),
			wantError: "instructions are required",
		},
		{
			name: "unknown policy schema", directory: "policy-skill",
			skill:     skillDocument("policy-skill", "description", "instructions"),
			policy:    "schema_version: other/v1\nallowed_tools: [query_logs]\n",
			wantError: "schema_version must be",
		},
		{
			name: "duplicate allowed tool", directory: "duplicate-tools",
			skill:     skillDocument("duplicate-tools", "description", "instructions"),
			policy:    "schema_version: talon-skill/v1\nallowed_tools: [query_logs, query_logs]\n",
			wantError: "duplicate allowed tool",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			directory := filepath.Join(root, test.directory)
			require.NoError(t, os.MkdirAll(directory, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(directory, SkillFileName), []byte(test.skill), 0o600))
			if test.policy != "" {
				require.NoError(t, os.WriteFile(filepath.Join(directory, PolicyFileName), []byte(test.policy), 0o600))
			}
			registry, err := LoadDirectory(root)
			assert.Nil(t, registry)
			require.ErrorContains(t, err, test.wantError)
		})
	}
}

func TestLoadDirectoryRejectsMissingDirectory(t *testing.T) {
	registry, err := LoadDirectory(filepath.Join(t.TempDir(), "missing"))
	assert.Nil(t, registry)
	require.ErrorContains(t, err, "stat skill directory")
}

func skillDocument(name, description, instructions string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" + instructions
}
