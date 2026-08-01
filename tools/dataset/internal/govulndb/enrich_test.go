package govulndb

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/tools/dataset/internal/githubapi"
)

type fakeCommitFetcher struct {
	results map[string]githubapi.FetchResult
	errors  map[string]error
}

func (fetcher *fakeCommitFetcher) GetCommit(_ context.Context, repository, commit string) (githubapi.FetchResult, error) {
	key := repository + "@" + commit
	if err := fetcher.errors[key]; err != nil {
		return githubapi.FetchResult{}, err
	}
	return fetcher.results[key], nil
}

func TestEnrichFileClassifiesAndSortsCandidates(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	input := filepath.Join(root, "candidates.jsonl")
	output := filepath.Join(root, "enriched.jsonl")
	candidates := []Candidate{
		{SchemaVersion: "v1", CandidateID: "GO-3@cccccccccccc", AdvisoryID: "GO-3", Repository: "acme/missing", FixCommit: "cccccccccccccccccccccccccccccccccccccccc"},
		{SchemaVersion: "v1", CandidateID: "GO-1@aaaaaaaaaaaa", AdvisoryID: "GO-1", Repository: "acme/good", FixCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{SchemaVersion: "v1", CandidateID: "GO-2@bbbbbbbbbbbb", AdvisoryID: "GO-2", Repository: "acme/large", FixCommit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}
	writeCandidateFixtures(t, input, candidates)

	good := githubapi.Commit{SHA: candidates[1].FixCommit, Message: "small fix", Stats: githubapi.CommitStats{Total: 12}}
	good.Parents = []githubapi.Parent{{SHA: "1111111111111111111111111111111111111111"}}
	good.Files = []githubapi.File{
		{Filename: "parser.go", Changes: 10, Patch: "@@ -1 +1 @@"},
		{Filename: "parser_test.go", Changes: 2, Patch: "@@ -1 +1 @@"},
	}
	large := githubapi.Commit{SHA: candidates[2].FixCommit, Message: "large merge", Stats: githubapi.CommitStats{Total: 900}}
	large.Parents = []githubapi.Parent{{SHA: "1"}, {SHA: "2"}}
	large.Files = []githubapi.File{
		{Filename: "README.md", Changes: 900, Patch: "@@ -1 +1 @@"},
		{Filename: "extra.txt"},
		{Filename: "docs/notice.txt"},
	}
	fetcher := &fakeCommitFetcher{
		results: map[string]githubapi.FetchResult{
			"acme/good@" + candidates[1].FixCommit:  {Commit: good, CacheHit: true},
			"acme/large@" + candidates[2].FixCommit: {Commit: large, FilesTruncated: true},
		},
		errors: map[string]error{"acme/missing@" + candidates[0].FixCommit: errors.New("commit not found")},
	}

	stats, err := EnrichFile(context.Background(), fetcher, EnrichOptions{
		InputPath: input, OutputPath: output, Concurrency: 3, MaxFiles: 2, MaxChanges: 400,
	})
	require.NoError(t, err)
	require.Equal(t, 3, stats.InputCandidates)
	require.Equal(t, 3, stats.ProcessedCandidates)
	require.Equal(t, 1, stats.MaterializableRecords)
	require.Equal(t, 1, stats.RejectedRecords)
	require.Equal(t, 1, stats.FetchFailedRecords)
	require.Equal(t, 1, stats.CacheHits)

	records := readEnrichedFixtures(t, output)
	require.Len(t, records, 3)
	outputData, err := os.ReadFile(output)
	require.NoError(t, err)
	require.NotContains(t, string(outputData), "cache_hit")
	require.Equal(t, "GO-1@aaaaaaaaaaaa", records[0].CandidateID)
	require.Equal(t, StatusMaterializable, records[0].Status)
	require.Equal(t, []string{"parser.go"}, records[0].ReviewableGoFiles)
	require.Equal(t, "GO-2@bbbbbbbbbbbb", records[1].CandidateID)
	require.Equal(t, StatusRejected, records[1].Status)
	require.Equal(t, []string{
		"parent_count_not_one", "changed_files_truncated", "too_many_changed_files",
		"too_many_changed_lines", "no_reviewable_go_file",
	}, records[1].RejectionReasons)
	require.Equal(t, StatusFetchFailed, records[2].Status)
	require.Equal(t, "commit not found", records[2].FetchError)
}

func TestIsReviewableGoFile(t *testing.T) {
	t.Parallel()

	accepted := []string{"main.go", "internal/parser/decode.go"}
	for _, path := range accepted {
		require.True(t, isReviewableGoFile(path), path)
	}
	rejected := []string{
		"README.md", "parser_test.go", "vendor/acme/a.go", "testdata/a.go",
		"api/generated/a.go", "message.pb.go", "types_generated.go", "zz_generated.deepcopy.go",
	}
	for _, path := range rejected {
		require.False(t, isReviewableGoFile(path), path)
	}
}

func writeCandidateFixtures(t *testing.T, path string, candidates []Candidate) {
	t.Helper()
	file, err := os.Create(path)
	require.NoError(t, err)
	encoder := json.NewEncoder(file)
	for _, candidate := range candidates {
		require.NoError(t, encoder.Encode(candidate))
	}
	require.NoError(t, file.Close())
}

func readEnrichedFixtures(t *testing.T, path string) []EnrichedCandidate {
	t.Helper()
	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()

	var records []EnrichedCandidate
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record EnrichedCandidate
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &record))
		records = append(records, record)
	}
	require.NoError(t, scanner.Err())
	return records
}
