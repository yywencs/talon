package main

import (
	"errors"
	"flag"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseOptionsUsesRunnableDefaults(t *testing.T) {
	result, err := parseOptions(nil)
	require.NoError(t, err)
	assert.Equal(t, "data/toolops-v1", result.datasetRoot)
	assert.Equal(t, "mapping-regression-rollback-001", result.scenarioID)
	assert.True(t, result.autoApprove)
	assert.Equal(t, 5*time.Minute, result.timeout)
}

func TestParseOptionsRejectsInvalidLimits(t *testing.T) {
	_, err := parseOptions([]string{"--timeout=0s"})
	require.EqualError(t, err, "timeout must be positive")
	_, err = parseOptions([]string{"--max-agent-steps=0"})
	require.EqualError(t, err, "max-agent-steps must be positive")
}

func TestParseOptionsDoesNotAllowDatabaseSelection(t *testing.T) {
	_, err := parseOptions([]string{"--database=talon.db"})
	require.Error(t, err)
}

func TestParseOptionsSupportsHelp(t *testing.T) {
	_, err := parseOptions([]string{"--help"})
	assert.True(t, errors.Is(err, flag.ErrHelp))
}

func TestParseOptionsSupportsVersion(t *testing.T) {
	result, err := parseOptions([]string{"--version"})
	require.NoError(t, err)
	assert.True(t, result.showVersion)
}

func TestParseOptionsSupportsListingScenarios(t *testing.T) {
	result, err := parseOptions([]string{"--dataset=data/toolops-v1", "--list-scenarios"})
	require.NoError(t, err)
	assert.True(t, result.listScenarios)
	assert.Equal(t, "data/toolops-v1", result.datasetRoot)
}
