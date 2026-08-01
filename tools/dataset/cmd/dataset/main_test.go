// 数据命令测试使用临时目录，确保不会读写项目中的真实 data 目录。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/tools/dataset/internal/govulndb"
)

func TestRunGoVulnDBFilter(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	input := filepath.Join(root, "ID")
	output := filepath.Join(root, "interim", "candidates.jsonl")
	require.NoError(t, os.MkdirAll(input, 0o755))
	record := `{
  "schema_version":"1.3.1",
  "id":"GO-2026-0100",
  "summary":"fixture",
  "affected":[{"package":{"name":"github.com/acme/service","ecosystem":"Go"}}],
  "references":[{"type":"FIX","url":"https://github.com/acme/service/commit/abcdef1234567890abcdef1234567890abcdef12"}],
  "database_specific":{"review_status":"REVIEWED"}
}`
	require.NoError(t, os.WriteFile(filepath.Join(input, "GO-2026-0100.json"), []byte(record), 0o644))

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{
		"govulndb-filter", "--input", input, "--output", output,
	}, &stdout, &stderr)
	require.NoError(t, err)
	require.Empty(t, stderr.String())

	var stats govulndb.Stats
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &stats))
	require.Equal(t, 1, stats.EligibleEntries)
	require.Equal(t, 1, stats.CandidateRecords)
	_, err = os.Stat(output)
	require.NoError(t, err)
}

func TestRunRejectsUnknownDatasetSubcommand(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"unknown"}, &stdout, &stderr)
	require.EqualError(t, err, `unknown subcommand "unknown"; available: govulndb-filter, govulndb-enrich, govulndb-select`)
}

func TestRunGoVulnDBSelectValidatesOptions(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{
		"govulndb-select", "--input", filepath.Join(t.TempDir(), "missing.jsonl"), "--size", "0",
	}, &stdout, &stderr)
	require.EqualError(t, err, "govulndb: selection size must be greater than zero")
	require.Empty(t, stdout.String())
}
