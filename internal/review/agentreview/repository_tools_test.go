package agentreview

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/tool/repository"
)

type fakeRepositoryReader struct {
	readInput   readRepositoryFileInput
	searchInput searchRepositorySymbolInput
	listInput   listRepositoryFilesInput
}

func (r *fakeRepositoryReader) ReadFile(_ context.Context, revision repository.Revision, path string, startLine, endLine int) (repository.FileSnippet, error) {
	r.readInput = readRepositoryFileInput{Revision: revision, Path: path, StartLine: startLine, EndLine: endLine}
	return repository.FileSnippet{
		Revision: revision, Path: path, StartLine: startLine, EndLine: endLine,
		Content: "10 | return sanitize(input)",
	}, nil
}

func (r *fakeRepositoryReader) SearchSymbol(_ context.Context, revision repository.Revision, symbol string) ([]repository.SearchResult, error) {
	r.searchInput = searchRepositorySymbolInput{Revision: revision, Symbol: symbol}
	return []repository.SearchResult{{Path: "safe.go", Line: 10, Content: "func sanitize(input string) string"}}, nil
}

func (r *fakeRepositoryReader) ListFiles(_ context.Context, revision repository.Revision, directory string) ([]string, error) {
	r.listInput = listRepositoryFilesInput{Revision: revision, Directory: directory}
	return []string{"internal/safe.go", "internal/safe_test.go"}, nil
}

func TestNewRepositoryToolsBuildsStableEinoTools(t *testing.T) {
	t.Parallel()
	reader := &fakeRepositoryReader{}
	tools, err := NewRepositoryTools(reader)
	require.NoError(t, err)
	require.Len(t, tools, 3)

	wantNames := []string{readRepositoryFileToolName, searchRepositoryToolName, listRepositoryFilesToolName}
	for index, tool := range tools {
		info, err := tool.Info(context.Background())
		require.NoError(t, err)
		require.Equal(t, wantNames[index], info.Name)
		require.NotNil(t, info.ParamsOneOf)
	}

	result, err := tools[0].InvokableRun(context.Background(), `{"revision":"head","path":"safe.go","start_line":10,"end_line":12}`)
	require.NoError(t, err)
	require.Equal(t, readRepositoryFileInput{Revision: repository.RevisionHead, Path: "safe.go", StartLine: 10, EndLine: 12}, reader.readInput)
	var snippet repository.FileSnippet
	require.NoError(t, json.Unmarshal([]byte(result), &snippet))
	require.Equal(t, "10 | return sanitize(input)", snippet.Content)

	result, err = tools[1].InvokableRun(context.Background(), `{"revision":"base","symbol":"sanitize"}`)
	require.NoError(t, err)
	require.Equal(t, searchRepositorySymbolInput{Revision: repository.RevisionBase, Symbol: "sanitize"}, reader.searchInput)
	var matches []repository.SearchResult
	require.NoError(t, json.Unmarshal([]byte(result), &matches))
	require.Equal(t, "safe.go", matches[0].Path)

	result, err = tools[2].InvokableRun(context.Background(), `{"revision":"head","directory":"internal"}`)
	require.NoError(t, err)
	require.Equal(t, listRepositoryFilesInput{Revision: repository.RevisionHead, Directory: "internal"}, reader.listInput)
	var files []string
	require.NoError(t, json.Unmarshal([]byte(result), &files))
	require.Equal(t, []string{"internal/safe.go", "internal/safe_test.go"}, files)
}

func TestNewRepositoryToolsRejectsMissingReader(t *testing.T) {
	t.Parallel()
	_, err := NewRepositoryTools(nil)
	require.ErrorContains(t, err, "repository reader is required")
}

func TestRepositoryToolRejectsMalformedArgumentsBeforeCallingReader(t *testing.T) {
	t.Parallel()
	reader := &fakeRepositoryReader{}
	tools, err := NewRepositoryTools(reader)
	require.NoError(t, err)
	_, err = tools[0].InvokableRun(context.Background(), `{"revision":"head","path":`)
	require.Error(t, err)
	require.Zero(t, reader.readInput)
}
