package revieweval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/review"
)

func TestReadRecordsAndMaterializeDiff(t *testing.T) {
	t.Parallel()
	records, err := ReadRecords(strings.NewReader(datasetJSONL("candidate-1", "modified")), 0)
	require.NoError(t, err)
	require.Len(t, records, 1)

	diff, err := MaterializeDiff(records[0])
	require.NoError(t, err)
	require.Contains(t, diff, "diff --git a/config.go b/config.go")
	require.Contains(t, diff, "+const APIKey = \"exposed-key\"")
	files, err := review.ParseUnifiedDiff(diff)
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Equal(t, "config.go", files[0].Path())
}

func TestReadRecordsHonorsLimit(t *testing.T) {
	t.Parallel()
	input := datasetJSONL("candidate-1", "modified") + datasetJSONL("candidate-2", "modified")
	records, err := ReadRecords(strings.NewReader(input), 1)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "candidate-1", records[0].CandidateID)
}

func TestReadRecordsRejectsUnknownSchema(t *testing.T) {
	t.Parallel()
	input := strings.Replace(datasetJSONL("candidate-1", "modified"), `"schema_version":"v1"`, `"schema_version":"v2"`, 1)
	_, err := ReadRecords(strings.NewReader(input), 0)
	require.ErrorContains(t, err, `unsupported schema_version "v2"`)
}

func TestRunRecordsFailuresAndContinues(t *testing.T) {
	t.Parallel()
	input := datasetJSONL("completed", "modified") + datasetJSONL("failed", "modified")
	records, err := ReadRecords(strings.NewReader(input), 0)
	require.NoError(t, err)

	var output bytes.Buffer
	summary, err := Run(context.Background(), records, func(_ context.Context, record Record) (review.Reviewer, error) {
		if record.CandidateID == "failed" {
			return nil, errors.New("model unavailable")
		}
		return review.NewRuleReviewer(), nil
	}, &output, Options{ReviewerName: "rules", PerSampleTimeout: time.Second})
	require.NoError(t, err)
	require.Equal(t, 2, summary.Total)
	require.Equal(t, 1, summary.Completed)
	require.Equal(t, 1, summary.Failed)
	require.Equal(t, 1, summary.Findings)
	require.Equal(t, 1, summary.Risks.High)

	decoder := json.NewDecoder(&output)
	var first, second Result
	require.NoError(t, decoder.Decode(&first))
	require.NoError(t, decoder.Decode(&second))
	require.Equal(t, "completed", first.Status)
	require.NotNil(t, first.Report)
	require.Equal(t, "failed", second.Status)
	require.Equal(t, "model unavailable", second.Error)
}

func TestMaterializeDiffSupportsAddedFiles(t *testing.T) {
	t.Parallel()
	records, err := ReadRecords(strings.NewReader(datasetJSONL("candidate-1", "added")), 0)
	require.NoError(t, err)
	diff, err := MaterializeDiff(records[0])
	require.NoError(t, err)
	require.Contains(t, diff, "diff --git a/config.go b/config.go")
	require.Contains(t, diff, "--- /dev/null\n+++ b/config.go")
}

func datasetJSONL(candidateID, status string) string {
	return `{"schema_version":"v1","candidate_id":"` + candidateID + `","advisory_id":"GO-TEST","repository":"acme/service","fix_commit":"2222222222222222222222222222222222222222","commit":{"parents":[{"sha":"1111111111111111111111111111111111111111"}],"files":[{"filename":"config.go","status":"` + status + `","patch":"@@ -1 +1,2 @@\n package config\n+const APIKey = \"exposed-key\""}]},"selection":{"rank":1}}` + "\n"
}
