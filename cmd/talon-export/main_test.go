package main

import (
	"errors"
	"flag"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestParseOptionsSupportsHelp(t *testing.T) {
	_, err := parseOptions([]string{"--help"})
	assert.True(t, errors.Is(err, flag.ErrHelp))
}
