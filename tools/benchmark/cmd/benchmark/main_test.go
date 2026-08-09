package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/tools/benchmark/internal/revieweval"
)

func TestRunEvaluatesDatasetAndWritesResults(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	datasetPath := filepath.Join(directory, "pilot.jsonl")
	outputPath := filepath.Join(directory, "results", "output.jsonl")
	require.NoError(t, os.WriteFile(datasetPath, []byte(testDataset), 0o600))

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{
		"review",
		"--dataset", datasetPath,
		"--output", outputPath,
		"--reviewer", "rules",
		"--progress=false",
		"--pretty=false",
	}, &stdout, &stderr)
	require.NoError(t, err)
	require.Empty(t, stderr.String())

	var summary commandSummary
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &summary))
	require.Equal(t, 1, summary.Total)
	require.Equal(t, 1, summary.Completed)
	require.Equal(t, 1, summary.Findings)
	require.Equal(t, outputPath, summary.Output)

	result, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	require.Contains(t, string(result), `"candidate_id":"candidate-1"`)
	require.Contains(t, string(result), `"risk":"high"`)
}

func TestRunRejectsRepositoryToolsForRules(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{
		"review", "--reviewer", "rules", "--repositories-root", t.TempDir(),
	}, &stdout, &stderr)
	require.EqualError(t, err, "repositories-root is only supported with reviewer agent")
}

func TestRunRequiresKnownSubcommand(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	require.EqualError(t, run(context.Background(), nil, &stdout, &stderr), "subcommand is required; available: review")
	require.EqualError(t, run(context.Background(), []string{"unknown"}, &stdout, &stderr), `unknown subcommand "unknown"; available: review`)
}

func TestRecordRepositoryRootUsesDatasetConvention(t *testing.T) {
	t.Parallel()
	records, err := evaluationRecords()
	require.NoError(t, err)
	root, err := recordRepositoryRoot("/tmp/review-repos", records[0])
	require.NoError(t, err)
	require.Equal(t, "/tmp/review-repos/01-acme__service", root)
}

func evaluationRecords() ([]revieweval.Record, error) {
	return revieweval.ReadRecords(bytes.NewBufferString(testDataset), 0)
}

const testDataset = `{"schema_version":"v1","candidate_id":"candidate-1","advisory_id":"GO-TEST","repository":"acme/service","fix_commit":"2222222222222222222222222222222222222222","commit":{"parents":[{"sha":"1111111111111111111111111111111111111111"}],"files":[{"filename":"config.go","status":"modified","patch":"@@ -1 +1,2 @@\n package config\n+const APIKey = \"exposed-key\""}]},"selection":{"rank":1}}
`
