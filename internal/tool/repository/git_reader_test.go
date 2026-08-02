package repository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGitReaderReadsOnlyBoundRevisions(t *testing.T) {
	t.Parallel()
	root, baseSHA, headSHA := createGitFixture(t)
	reader, err := NewGitReader(context.Background(), GitConfig{
		RepositoryRoot: root, BaseSHA: baseSHA, HeadSHA: headSHA,
	})
	require.NoError(t, err)

	base, err := reader.ReadFile(context.Background(), RevisionBase, "safe.go", 1, 3)
	require.NoError(t, err)
	require.Equal(t, RevisionBase, base.Revision)
	require.Equal(t, "safe.go", base.Path)
	require.Contains(t, base.Content, "2 | func URLEscape")
	require.Contains(t, base.Content, "return input")

	head, err := reader.ReadFile(context.Background(), RevisionHead, "safe.go", 2, 4)
	require.NoError(t, err)
	require.Contains(t, head.Content, "return sanitize(input)")

	_, err = reader.ReadFile(context.Background(), Revision("main"), "safe.go", 1, 1)
	require.ErrorContains(t, err, `revision must be "base" or "head"`)
}

func TestGitReaderRejectsUnsafePathsAndNonTextFiles(t *testing.T) {
	t.Parallel()
	root, baseSHA, headSHA := createGitFixture(t)
	reader, err := NewGitReader(context.Background(), GitConfig{
		RepositoryRoot: root, BaseSHA: baseSHA, HeadSHA: headSHA,
	})
	require.NoError(t, err)

	for _, unsafePath := range []string{"../outside", "nested/../safe.go", "/etc/passwd", `.git/config`, `nested\\file.go`, "safe.go\nother.go", "safe.go "} {
		_, err := reader.ReadFile(context.Background(), RevisionHead, unsafePath, 1, 2)
		require.Error(t, err, unsafePath)
	}
	_, err = reader.ReadFile(context.Background(), RevisionHead, "outside-link", 1, 2)
	require.ErrorContains(t, err, "not a regular file")
	_, err = reader.ReadFile(context.Background(), RevisionHead, "binary.bin", 1, 2)
	require.ErrorContains(t, err, "not UTF-8 text")
}

func TestGitReaderSearchesSymbolsAndListsBoundedFiles(t *testing.T) {
	t.Parallel()
	root, baseSHA, headSHA := createGitFixture(t)
	reader, err := NewGitReader(context.Background(), GitConfig{
		RepositoryRoot: root, BaseSHA: baseSHA, HeadSHA: headSHA,
		Limits: Limits{MaxSearchResult: 1, MaxListedFiles: 2},
	})
	require.NoError(t, err)

	results, err := reader.SearchSymbol(context.Background(), RevisionHead, "URLEscape")
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, SearchResult{Path: "safe.go", Line: 2, Content: "func URLEscape(input string) string {"}, results[0])

	missing, err := reader.SearchSymbol(context.Background(), RevisionHead, "DoesNotExist")
	require.NoError(t, err)
	require.NotNil(t, missing)
	require.Empty(t, missing)

	_, err = reader.SearchSymbol(context.Background(), RevisionHead, "unsafe.*regex")
	require.ErrorContains(t, err, "Go-like identifier")

	files, err := reader.ListFiles(context.Background(), RevisionHead, ".")
	require.NoError(t, err)
	require.Len(t, files, 2)
	require.IsIncreasing(t, files)

	nested, err := reader.ListFiles(context.Background(), RevisionHead, "nested")
	require.NoError(t, err)
	require.Equal(t, []string{"nested/more.go"}, nested)
}

func TestGitReaderEnforcesLineAndFileLimits(t *testing.T) {
	t.Parallel()
	root, baseSHA, headSHA := createGitFixture(t)
	reader, err := NewGitReader(context.Background(), GitConfig{
		RepositoryRoot: root, BaseSHA: baseSHA, HeadSHA: headSHA,
		Limits: Limits{MaxReadLines: 2, MaxFileBytes: 8},
	})
	require.NoError(t, err)

	_, err = reader.ReadFile(context.Background(), RevisionHead, "safe.go", 1, 3)
	require.ErrorContains(t, err, "1-2 lines")
	_, err = reader.ReadFile(context.Background(), RevisionHead, "safe.go", 1, 2)
	require.ErrorContains(t, err, "exceeds 8-byte limit")
}

func TestNewGitReaderRejectsAbbreviatedSHA(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_, err := NewGitReader(context.Background(), GitConfig{
		RepositoryRoot: root, BaseSHA: "abcdef1", HeadSHA: "abcdef2",
	})
	require.ErrorContains(t, err, "full 40-character")
}

func createGitFixture(t *testing.T) (root, baseSHA, headSHA string) {
	t.Helper()
	root = t.TempDir()
	runGitFixture(t, root, "init", "--quiet")
	require.NoError(t, os.WriteFile(filepath.Join(root, "safe.go"), []byte("package sample\nfunc URLEscape(input string) string {\n\treturn input\n}\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "nested", "more.go"), []byte("package nested\nfunc Other() {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "binary.bin"), []byte{'a', 0, 'b'}, 0o644))
	require.NoError(t, os.Symlink("../outside-secret", filepath.Join(root, "outside-link")))
	runGitFixture(t, root, "add", "--", ".")
	runGitFixture(t, root, "commit", "--quiet", "-m", "base")
	baseSHA = runGitFixture(t, root, "rev-parse", "HEAD")

	require.NoError(t, os.WriteFile(filepath.Join(root, "safe.go"), []byte("package sample\nfunc URLEscape(input string) string {\n\treturn sanitize(input)\n}\nfunc sanitize(input string) string { return input }\n"), 0o644))
	runGitFixture(t, root, "add", "--", "safe.go")
	runGitFixture(t, root, "commit", "--quiet", "-m", "head")
	headSHA = runGitFixture(t, root, "rev-parse", "HEAD")
	return root, baseSHA, headSHA
}

func runGitFixture(t *testing.T, root string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", root}, args...)
	command := exec.Command("git", commandArgs...)
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=OpenTalon Test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=OpenTalon Test", "GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	return string(bytesTrimSpace(output))
}

func bytesTrimSpace(value []byte) []byte {
	start, end := 0, len(value)
	for start < end && (value[start] == ' ' || value[start] == '\n' || value[start] == '\r' || value[start] == '\t') {
		start++
	}
	for end > start && (value[end-1] == ' ' || value[end-1] == '\n' || value[end-1] == '\r' || value[end-1] == '\t') {
		end--
	}
	return value[start:end]
}
