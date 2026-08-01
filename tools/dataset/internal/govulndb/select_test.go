package govulndb

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/tools/dataset/internal/githubapi"
)

func TestClassifyVulnerability(t *testing.T) {
	t.Parallel()
	tests := []struct {
		summary  string
		category string
	}{
		{"SQL injection in query builder", CategoryInjection},
		{"Path traversal during archive extraction", CategoryFilesystem},
		{"Server-side request forgery in proxy", CategoryNetwork},
		{"Improper TLS certificate validation", CategoryCrypto},
		{"Authentication bypass permits account access", CategoryAuth},
		{"Denial of service through an infinite loop", CategoryDoSMemory},
		{"Incorrect parser result", CategoryOther},
	}
	for _, test := range tests {
		test := test
		t.Run(test.category, func(t *testing.T) {
			t.Parallel()
			record := EnrichedCandidate{Candidate: Candidate{Summary: test.summary}}
			require.Equal(t, test.category, classifyVulnerability(record))
		})
	}
}

func TestAssessForSelectionRequiresTestAndSymbolEvidence(t *testing.T) {
	t.Parallel()
	record := selectionFixture(1, CategoryInjection, 2024)
	record.Commit.Files = record.Commit.Files[:1]
	record.Commit.Files[0].Patch = "@@ -1 +1 @@\n-func Safe() {}\n+func StillSafe() {}"

	_, reasons := assessForSelection(record, "seed")
	require.ElementsMatch(t, []string{"affected_symbol_not_in_patch", "changed_test_file_missing"}, reasons)
}

func TestSelectFileIsDeterministicAndDiverse(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	input := filepath.Join(root, "enriched.jsonl")
	records := make([]EnrichedCandidate, 0, 21)
	for categoryIndex, category := range categoryOrder {
		for variant := 0; variant < 3; variant++ {
			index := categoryIndex*3 + variant + 1
			records = append(records, selectionFixture(index, category, 2021+(index%5)))
		}
	}
	_, err := writeEnrichedJSONLAtomic(input, records)
	require.NoError(t, err)

	firstOutput := filepath.Join(root, "first.jsonl")
	secondOutput := filepath.Join(root, "second.jsonl")
	first, err := SelectFile(SelectOptions{InputPath: input, OutputPath: firstOutput, Size: 15, Seed: "stable-seed"})
	require.NoError(t, err)
	second, err := SelectFile(SelectOptions{InputPath: input, OutputPath: secondOutput, Size: 15, Seed: "stable-seed"})
	require.NoError(t, err)

	require.Equal(t, first.OutputSHA256, second.OutputSHA256)
	firstData, err := os.ReadFile(firstOutput)
	require.NoError(t, err)
	secondData, err := os.ReadFile(secondOutput)
	require.NoError(t, err)
	require.Equal(t, firstData, secondData)
	require.Equal(t, 21, first.StrictCandidates)
	require.Equal(t, 15, first.SelectedRecords)
	require.Equal(t, 15, first.UniqueRepositories)
	require.Equal(t, 3, first.CategoryCounts[CategoryDoSMemory])
	for _, category := range categoryOrder {
		if category != CategoryDoSMemory {
			require.Equal(t, 2, first.CategoryCounts[category])
		}
	}
	for _, count := range first.YearCounts {
		require.LessOrEqual(t, count, 5)
	}
}

func selectionFixture(index int, category string, year int) EnrichedCandidate {
	summaries := map[string]string{
		CategoryInjection:  "SQL injection in query builder",
		CategoryAuth:       "Authentication bypass allows access",
		CategoryNetwork:    "Server-side request forgery in proxy",
		CategoryFilesystem: "Path traversal during archive extraction",
		CategoryCrypto:     "TLS certificate validation issue",
		CategoryDoSMemory:  "Denial of service through infinite loop",
		CategoryOther:      "Incorrect parser result",
	}
	commit := &githubapi.Commit{
		SHA:     fmt.Sprintf("%040x", index),
		Parents: []githubapi.Parent{{SHA: fmt.Sprintf("%040x", index+100)}},
		Stats:   githubapi.CommitStats{Additions: 8, Deletions: 2, Total: 10},
		Files: []githubapi.File{
			{Filename: "review.go", Status: "modified", Changes: 5, Patch: "@@ -1 +1 @@\n-func Vulnerable() {}\n+func Vulnerable() { validate() }"},
			{Filename: "review_test.go", Status: "modified", Changes: 5, Patch: "@@ -1 +1 @@\n-func TestOld() {}\n+func TestVulnerable() {}"},
		},
	}
	return EnrichedCandidate{
		Candidate: Candidate{
			SchemaVersion: CandidateSchemaVersion,
			CandidateID:   fmt.Sprintf("GO-%d-%04d@%012x", year, index, index),
			AdvisoryID:    fmt.Sprintf("GO-%d-%04d", year, index), Aliases: []string{fmt.Sprintf("GHSA-fixture-%04d", index)},
			Summary: summaries[category], Published: fmt.Sprintf("%d-01-01T00:00:00Z", year),
			Modules:         []string{fmt.Sprintf("github.com/example/repo-%d", index)},
			AffectedImports: []AffectedImport{{Path: "example", Symbols: []string{"Vulnerable"}}},
			Repository:      fmt.Sprintf("example/repo-%d", index), FixCommit: commit.SHA,
			FixURL: "https://github.com/example/repo/commit/fixture", ReviewStatus: "REVIEWED",
			SourceFile: "fixture.json", SourceSHA256: fmt.Sprintf("%064x", index), SourceLicense: "CC-BY-4.0",
		},
		EnrichmentSchemaVersion: EnrichmentSchemaVersion,
		Status:                  StatusMaterializable, ReviewableGoFiles: []string{"review.go"}, Commit: commit,
	}
}
