package githubapi

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestGetCommitFetchesOnceThenUsesTokenFreeCache(t *testing.T) {
	t.Parallel()

	const token = "secret-test-token"
	const commit = "abcdef1234567890abcdef1234567890abcdef12"
	var calls atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		require.Equal(t, "Bearer "+token, request.Header.Get("Authorization"))
		require.Equal(t, apiVersion, request.Header.Get("X-GitHub-Api-Version"))
		require.Equal(t, "/repos/acme/service/commits/"+commit, request.URL.Path)
		headers := make(http.Header)
		headers.Set("Link", `<https://api.github.test/next>; rel="next"`)
		headers.Set("X-RateLimit-Remaining", "4991")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     headers,
			Body: io.NopCloser(strings.NewReader(`{
  "sha":"abcdef1234567890abcdef1234567890abcdef12",
  "commit":{"message":"security fix","author":{"date":"2026-01-01T00:00:00Z"},"committer":{"date":"2026-01-02T00:00:00Z"}},
  "parents":[{"sha":"1111111111111111111111111111111111111111"}],
  "stats":{"additions":10,"deletions":2,"total":12},
  "files":[{"filename":"parser.go","status":"modified","additions":10,"deletions":2,"changes":12,"patch":"@@ -1 +1 @@"}]
}`)),
		}, nil
	})
	cacheDir := t.TempDir()
	client, err := NewClient(Config{
		Token: token, CacheDir: cacheDir, BaseURL: "https://api.github.test",
		HTTPClient: &http.Client{Transport: transport},
	})
	require.NoError(t, err)

	first, err := client.GetCommit(context.Background(), "acme/service", commit)
	require.NoError(t, err)
	require.False(t, first.CacheHit)
	require.True(t, first.FilesTruncated)
	require.Equal(t, 4991, first.RateRemaining)
	require.Equal(t, "security fix", first.Commit.Message)

	second, err := client.GetCommit(context.Background(), "acme/service", commit)
	require.NoError(t, err)
	require.True(t, second.CacheHit)
	require.True(t, second.FilesTruncated)
	require.Equal(t, int32(1), calls.Load())

	cachePath := filepath.Join(cacheDir, "acme__service", commit+".json")
	cacheData, err := os.ReadFile(cachePath)
	require.NoError(t, err)
	require.NotContains(t, string(cacheData), token)
}

func TestGetCommitRejectsInvalidIdentifiers(t *testing.T) {
	t.Parallel()

	client, err := NewClient(Config{Token: "token", CacheDir: t.TempDir()})
	require.NoError(t, err)
	_, err = client.GetCommit(context.Background(), "invalid", "abcdef1234567")
	require.ErrorContains(t, err, "invalid repository")
	_, err = client.GetCommit(context.Background(), "acme/service", "not-a-commit")
	require.ErrorContains(t, err, "invalid commit")
}

func TestResolveTokenPrefersGitHubCLIEnvironmentOrder(t *testing.T) {
	t.Setenv("GH_TOKEN", "gh-token")
	t.Setenv("GITHUB_TOKEN", "github-token")
	token, err := ResolveToken(context.Background())
	require.NoError(t, err)
	require.Equal(t, "gh-token", token)
}
