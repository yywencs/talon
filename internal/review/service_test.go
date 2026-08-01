package review

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServiceProducesDeterministicReviewReport(t *testing.T) {
	t.Parallel()

	diff := `diff --git a/client.go b/client.go
--- a/client.go
+++ b/client.go
@@ -3,2 +3,4 @@ import "crypto/tls"
 var client = &http.Client{}
+var password = "super-secret"
+var tlsConfig = &tls.Config{InsecureSkipVerify: true}
`
	service := NewService(NewRuleReviewer())
	report, err := service.Review(context.Background(), Request{
		Repository: "acme/service", PullRequest: 42,
		BaseSHA: "base", HeadSHA: "head", Language: "go", Diff: diff,
	})
	require.NoError(t, err)
	require.Equal(t, SchemaVersion, report.SchemaVersion)
	require.Equal(t, "acme/service", report.Repository)
	require.Equal(t, 42, report.PullRequest)
	require.Equal(t, "high", report.Risk)
	require.Equal(t, 1, report.FilesReviewed)
	require.Equal(t, "deterministic-rules/v1", report.Reviewer)
	require.Equal(t, Summary{Total: 2, High: 2}, report.Summary)
	require.Len(t, report.Findings, 2)
	require.Equal(t, "SEC-HARDCODED-SECRET", report.Findings[0].RuleID)
	require.Equal(t, 4, report.Findings[0].StartLine)
	require.Equal(t, "SEC-TLS-SKIP-VERIFY", report.Findings[1].RuleID)
	require.Equal(t, 5, report.Findings[1].StartLine)
}

func TestServiceReturnsEmptyFindingArrayForCleanDiff(t *testing.T) {
	t.Parallel()

	diff := `diff --git a/hello.go b/hello.go
--- a/hello.go
+++ b/hello.go
@@ -1 +1,2 @@
 package hello
+const Name = "OpenTalon"
`
	report, err := NewService(NewRuleReviewer()).Review(context.Background(), Request{Diff: diff})
	require.NoError(t, err)
	require.Equal(t, "none", report.Risk)
	require.Zero(t, report.Summary.Total)
	require.NotNil(t, report.Findings)
	require.Empty(t, report.Findings)
}

func TestServiceHonorsCancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewService(NewRuleReviewer()).Review(ctx, Request{Diff: "unused"})
	require.ErrorIs(t, err, context.Canceled)
}
