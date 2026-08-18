package main

import (
	"context"
	"errors"
	"flag"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/runartifact"
)

type preflightStore struct {
	outcomes []string
}

func (s *preflightStore) Upsert(context.Context, runartifact.RunArtifact) error { return nil }
func (s *preflightStore) Get(context.Context, string) (runartifact.RunArtifact, error) {
	return runartifact.RunArtifact{}, runartifact.ErrNotFound
}
func (s *preflightStore) List(_ context.Context, filter runartifact.VersionFilter) ([]runartifact.RunArtifact, error) {
	if filter.Outcome == "" {
		return nil, errors.New("outcome is required")
	}
	s.outcomes = append(s.outcomes, filter.Outcome)
	return nil, nil
}

func TestParseOptionsRequiresVersionCohort(t *testing.T) {
	_, err := parseOptions(nil)
	require.EqualError(t, err, "dataset-version is required")
	_, err = parseOptions([]string{"--dataset-version=toolops-v1"})
	require.EqualError(t, err, "code-version is required")
	_, err = parseOptions([]string{"--dataset-version=toolops-v1", "--code-version=abc123"})
	require.EqualError(t, err, "output is required")
}

func TestParseOptionsAcceptsVersionCohort(t *testing.T) {
	result, err := parseOptions([]string{
		"--dataset-version=toolops-v1", "--code-version=abc123", "--output=exports/batch",
	})
	require.NoError(t, err)
	assert.Equal(t, "data", result.dataRoot)
	assert.Equal(t, "toolops-v1", result.datasetVersion)
	assert.Equal(t, "abc123", result.codeVersion)
}

func TestParseOptionsAllowsPreflightWithoutOutput(t *testing.T) {
	result, err := parseOptions([]string{
		"--dataset-version=toolops-v1",
		"--code-version=talon-toolops-agent/eval-test",
		"--preflight",
	})
	require.NoError(t, err)
	assert.True(t, result.preflight)
	assert.Empty(t, result.outputDir)
}

func TestParseOptionsSupportsHelp(t *testing.T) {
	_, err := parseOptions([]string{"--help"})
	assert.True(t, errors.Is(err, flag.ErrHelp))
}

func TestRunPreflightChecksEveryPersistedOutcome(t *testing.T) {
	store := &preflightStore{}
	err := runPreflight(context.Background(), store, options{
		dataRoot: "../../data", datasetVersion: "toolops-v1", codeVersion: "talon-toolops-agent/eval-test",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"running", "completed", "failed"}, store.outcomes)
}
