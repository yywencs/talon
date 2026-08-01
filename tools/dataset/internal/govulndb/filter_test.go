package govulndb

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFilterDirectoryFiltersExpandsAndDeduplicatesCandidates 覆盖完整的候选筛选顺序。
func TestFilterDirectoryFiltersExpandsAndDeduplicatesCandidates(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	input := filepath.Join(root, "go-vulndb", "ID")
	require.NoError(t, os.MkdirAll(input, 0o755))
	writeOSVFixture(t, input, "GO-2026-0001.json", map[string]any{
		"schema_version": "1.3.1",
		"id":             "GO-2026-0001",
		"modified":       "2026-01-02T00:00:00Z",
		"published":      "2026-01-01T00:00:00Z",
		"aliases":        []string{"GHSA-test-0001", "GHSA-test-0001"},
		"summary":        "reviewed vulnerability",
		"details":        "fixture details",
		"affected": []any{
			map[string]any{
				"package": map[string]any{"name": "github.com/acme/service", "ecosystem": "Go"},
				"ecosystem_specific": map[string]any{"imports": []any{
					map[string]any{"path": "github.com/acme/service/parser", "symbols": []string{"Decode", "Decode"}},
				}},
			},
		},
		"references": []any{
			map[string]any{"type": "ADVISORY", "url": "https://example.test/advisory"},
			map[string]any{"type": "FIX", "url": "https://github.com/acme/service/commit/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			map[string]any{"type": "FIX", "url": "https://github.com/acme/service/commit/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
			map[string]any{"type": "FIX", "url": "https://github.com/acme/service/commit/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
		// 按 Go 官方约定，缺少 review_status 仍然属于 REVIEWED。
		"database_specific": map[string]any{"url": "https://pkg.go.dev/vuln/GO-2026-0001"},
	})
	writeOSVFixture(t, input, "GO-2026-0002.json", fixtureEntry("GO-2026-0002", "UNREVIEWED", "github.com/acme/unreviewed", "https://github.com/acme/unreviewed/commit/cccccccccccccccccccccccccccccccccccccccc"))
	withdrawn := fixtureEntry("GO-2026-0003", "REVIEWED", "github.com/acme/withdrawn", "https://github.com/acme/withdrawn/commit/dddddddddddddddddddddddddddddddddddddddd")
	withdrawn["withdrawn"] = "2026-01-03T00:00:00Z"
	writeOSVFixture(t, input, "GO-2026-0003.json", withdrawn)
	writeOSVFixture(t, input, "GO-2026-0004.json", fixtureEntry("GO-2026-0004", "REVIEWED", "stdlib", "https://github.com/golang/go/commit/eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"))
	writeOSVFixture(t, input, "GO-2026-0005.json", fixtureEntry("GO-2026-0005", "REVIEWED", "github.com/acme/pull-only", "https://github.com/acme/pull-only/pull/12"))

	output := filepath.Join(root, "interim", "candidates.jsonl")
	stats, err := FilterDirectory(context.Background(), input, output)
	require.NoError(t, err)
	require.Equal(t, 5, stats.TotalEntries)
	require.Equal(t, 1, stats.WithdrawnEntries)
	require.Equal(t, 1, stats.UnreviewedEntries)
	require.Equal(t, 1, stats.NoExternalModuleEntries)
	require.Equal(t, 1, stats.NoGitHubCommitFixEntries)
	require.Equal(t, 1, stats.EligibleEntries)
	require.Equal(t, 2, stats.CandidateRecords)

	candidates := readCandidates(t, output)
	require.Len(t, candidates, 2)
	require.Equal(t, "GO-2026-0001@aaaaaaaaaaaa", candidates[0].CandidateID)
	require.Equal(t, "GO-2026-0001@bbbbbbbbbbbb", candidates[1].CandidateID)
	require.Equal(t, []string{"github.com/acme/service"}, candidates[0].Modules)
	require.Equal(t, []string{"GHSA-test-0001"}, candidates[0].Aliases)
	require.Equal(t, "acme/service", candidates[0].Repository)
	require.Equal(t, "ID/GO-2026-0001.json", candidates[0].SourceFile)
	require.Equal(t, sourceLicense, candidates[0].SourceLicense)
	require.Equal(t, []AffectedImport{{
		Path: "github.com/acme/service/parser", Symbols: []string{"Decode"},
	}}, candidates[0].AffectedImports)

	contents, err := os.ReadFile(output)
	require.NoError(t, err)
	digest := sha256.Sum256(contents)
	require.Equal(t, hex.EncodeToString(digest[:]), stats.OutputSHA256)
}

func TestFilterDirectoryIsDeterministic(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	input := filepath.Join(root, "go-vulndb", "ID")
	require.NoError(t, os.MkdirAll(input, 0o755))
	writeOSVFixture(t, input, "GO-2026-0010.json", fixtureEntry(
		"GO-2026-0010", "REVIEWED", "github.com/acme/service",
		"https://github.com/acme/service/commit/1234567890abcdef1234567890abcdef12345678",
	))

	first := filepath.Join(root, "first.jsonl")
	second := filepath.Join(root, "second.jsonl")
	firstStats, err := FilterDirectory(context.Background(), input, first)
	require.NoError(t, err)
	secondStats, err := FilterDirectory(context.Background(), input, second)
	require.NoError(t, err)
	require.Equal(t, firstStats.OutputSHA256, secondStats.OutputSHA256)
	firstData, err := os.ReadFile(first)
	require.NoError(t, err)
	secondData, err := os.ReadFile(second)
	require.NoError(t, err)
	require.Equal(t, firstData, secondData)
}

func TestParseGitHubCommitURL(t *testing.T) {
	t.Parallel()

	fix, ok := parseGitHubCommitURL("https://github.com/Acme/service.git/commit/ABCDEF1234567/?source=osv#diff")
	require.True(t, ok)
	require.Equal(t, "Acme/service", fix.Repository)
	require.Equal(t, "abcdef1234567", fix.Commit)

	invalid := []string{
		"http://github.com/acme/service/commit/abcdef1234567",
		"https://gitlab.com/acme/service/commit/abcdef1234567",
		"https://github.com/acme/service/pull/123",
		"https://github.com/acme/service/commit/not-a-sha",
		"https://github.com/acme/service/commit/abcdef1234567.patch",
	}
	for _, raw := range invalid {
		_, ok := parseGitHubCommitURL(raw)
		require.False(t, ok, raw)
	}
}

func fixtureEntry(id, status, module, fixURL string) map[string]any {
	return map[string]any{
		"schema_version": "1.3.1",
		"id":             id,
		"summary":        "fixture",
		"affected": []any{map[string]any{
			"package": map[string]any{"name": module, "ecosystem": "Go"},
		}},
		"references":        []any{map[string]any{"type": "FIX", "url": fixURL}},
		"database_specific": map[string]any{"review_status": status},
	}
}

func writeOSVFixture(t *testing.T, directory, name string, entry map[string]any) {
	t.Helper()
	data, err := json.Marshal(entry)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, name), data, 0o644))
}

func readCandidates(t *testing.T, path string) []Candidate {
	t.Helper()
	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()

	var candidates []Candidate
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var candidate Candidate
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &candidate))
		candidates = append(candidates, candidate)
	}
	require.NoError(t, scanner.Err())
	return candidates
}
