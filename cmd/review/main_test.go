package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/review"
)

func TestRunReviewsDiffFromStdin(t *testing.T) {
	t.Parallel()

	diff := `diff --git a/config.go b/config.go
--- a/config.go
+++ b/config.go
@@ -1 +1,2 @@
 package config
+const APIKey = "exposed-key"
`
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{
		"--repository", "acme/service", "--pr", "7", "--pretty=false",
	}, strings.NewReader(diff), &stdout, &stderr)
	require.NoError(t, err)
	require.Empty(t, stderr.String())

	var report review.Report
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &report))
	require.Equal(t, "acme/service", report.Repository)
	require.Equal(t, 7, report.PullRequest)
	require.Equal(t, "high", report.Risk)
	require.Len(t, report.Findings, 1)
	require.Equal(t, "SEC-HARDCODED-SECRET", report.Findings[0].RuleID)
}

func TestRunRejectsDiffOverSizeLimit(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"--max-diff-bytes", "4"}, strings.NewReader("12345"), &stdout, &stderr)
	require.EqualError(t, err, "diff exceeds 4-byte limit")
	require.Empty(t, stdout.String())
}

func TestRunRejectsUnexpectedArguments(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"unexpected"}, strings.NewReader(""), &stdout, &stderr)
	require.ErrorContains(t, err, "unexpected positional arguments")
}
