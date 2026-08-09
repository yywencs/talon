// Package revieweval replays versioned JSONL review datasets through a
// Reviewer and emits one machine-readable result per candidate.
package revieweval

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/wen/opentalon/internal/review"
)

const (
	ResultSchemaVersion = "v1"
	maxDatasetLineBytes = 16 << 20
)

// Record is the subset of review-v1 dataset fields required for replay.
type Record struct {
	SchemaVersion string `json:"schema_version"`
	CandidateID   string `json:"candidate_id"`
	AdvisoryID    string `json:"advisory_id"`
	Repository    string `json:"repository"`
	FixCommit     string `json:"fix_commit"`
	Commit        struct {
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
		Files []FilePatch `json:"files"`
	} `json:"commit"`
	Selection struct {
		Rank int `json:"rank"`
	} `json:"selection"`
}

// FilePatch contains one GitHub commit file patch.
type FilePatch struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename,omitempty"`
	Status           string `json:"status"`
	Patch            string `json:"patch,omitempty"`
}

// BaseSHA returns the single parent of the fix commit.
func (r Record) BaseSHA() string {
	if len(r.Commit.Parents) != 1 {
		return ""
	}
	return r.Commit.Parents[0].SHA
}

// ReviewerFactory binds a reviewer to a candidate. Repository-tool mode uses
// this hook to create a request-scoped reader for each repository.
type ReviewerFactory func(ctx context.Context, record Record) (review.Reviewer, error)

// Options controls one sequential, reproducible dataset replay.
type Options struct {
	ReviewerName     string
	PerSampleTimeout time.Duration
	Progress         func(completed, total int, result Result)
}

// Result is one JSONL row. Review failures are data, not fatal runner errors,
// so the remaining candidates can still be evaluated.
type Result struct {
	SchemaVersion string         `json:"schema_version"`
	CandidateID   string         `json:"candidate_id"`
	AdvisoryID    string         `json:"advisory_id"`
	Repository    string         `json:"repository"`
	Rank          int            `json:"rank,omitempty"`
	BaseSHA       string         `json:"base_sha"`
	HeadSHA       string         `json:"head_sha"`
	Status        string         `json:"status"`
	DurationMS    int64          `json:"duration_ms"`
	Error         string         `json:"error,omitempty"`
	Report        *review.Report `json:"report,omitempty"`
}

// RiskCounts provides stable summary fields instead of a free-form map.
type RiskCounts struct {
	None     int `json:"none"`
	Low      int `json:"low"`
	Medium   int `json:"medium"`
	High     int `json:"high"`
	Critical int `json:"critical"`
}

// Summary describes the completed replay and is printed by the CLI.
type Summary struct {
	SchemaVersion string     `json:"schema_version"`
	Reviewer      string     `json:"reviewer"`
	Total         int        `json:"total"`
	Completed     int        `json:"completed"`
	Failed        int        `json:"failed"`
	Findings      int        `json:"findings"`
	Risks         RiskCounts `json:"risks"`
	DurationMS    int64      `json:"duration_ms"`
}

// ReadRecords decodes and validates JSONL records. limit=0 reads all rows.
func ReadRecords(reader io.Reader, limit int) ([]Record, error) {
	if reader == nil {
		return nil, fmt.Errorf("evaluation: dataset reader is required")
	}
	if limit < 0 {
		return nil, fmt.Errorf("evaluation: limit cannot be negative")
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxDatasetLineBytes)
	records := make([]Record, 0)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var record Record
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, fmt.Errorf("evaluation: decode dataset line %d: %w", lineNumber, err)
		}
		if err := validateRecord(record); err != nil {
			return nil, fmt.Errorf("evaluation: dataset line %d: %w", lineNumber, err)
		}
		records = append(records, record)
		if limit > 0 && len(records) == limit {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("evaluation: scan dataset: %w", err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("evaluation: dataset contains no records")
	}
	return records, nil
}

// MaterializeDiff converts GitHub's file-level patches into a complete unified
// diff accepted by review.ParseUnifiedDiff.
func MaterializeDiff(record Record) (string, error) {
	if err := validateRecord(record); err != nil {
		return "", err
	}

	var builder strings.Builder
	patches := 0
	for _, file := range record.Commit.Files {
		oldPath, newPath, err := patchPaths(file)
		if err != nil {
			return "", err
		}
		headerOld, headerNew := oldPath, newPath
		if headerOld == "" {
			headerOld = headerNew
		}
		if headerNew == "" {
			headerNew = headerOld
		}
		fmt.Fprintf(&builder, "diff --git %s %s\n", formatPath("a/"+headerOld), formatPath("b/"+headerNew))
		if oldPath == "" {
			builder.WriteString("--- /dev/null\n")
		} else {
			fmt.Fprintf(&builder, "--- %s\n", formatPath("a/"+oldPath))
		}
		if newPath == "" {
			builder.WriteString("+++ /dev/null\n")
		} else {
			fmt.Fprintf(&builder, "+++ %s\n", formatPath("b/"+newPath))
		}
		if strings.TrimSpace(file.Patch) == "" {
			continue
		}
		builder.WriteString(file.Patch)
		if !strings.HasSuffix(file.Patch, "\n") {
			builder.WriteByte('\n')
		}
		patches++
	}
	if patches == 0 {
		return "", fmt.Errorf("evaluation: candidate %q has no materializable patches", record.CandidateID)
	}
	return builder.String(), nil
}

// Run evaluates records sequentially and writes one Result JSON object per
// line. A failed sample is recorded and does not stop the remaining replay.
func Run(ctx context.Context, records []Record, factory ReviewerFactory, output io.Writer, options Options) (Summary, error) {
	if len(records) == 0 {
		return Summary{}, fmt.Errorf("evaluation: records are required")
	}
	if factory == nil {
		return Summary{}, fmt.Errorf("evaluation: reviewer factory is required")
	}
	if output == nil {
		return Summary{}, fmt.Errorf("evaluation: result writer is required")
	}
	if options.PerSampleTimeout <= 0 {
		return Summary{}, fmt.Errorf("evaluation: per-sample timeout must be greater than zero")
	}

	started := time.Now()
	summary := Summary{SchemaVersion: ResultSchemaVersion, Reviewer: options.ReviewerName, Total: len(records)}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	for index, record := range records {
		if err := ctx.Err(); err != nil {
			return Summary{}, err
		}
		result := evaluateOne(ctx, record, factory, options.PerSampleTimeout)
		if result.Status == "completed" {
			summary.Completed++
			summary.Findings += result.Report.Summary.Total
			addRisk(&summary.Risks, result.Report.Risk)
		} else {
			summary.Failed++
		}
		if err := encoder.Encode(result); err != nil {
			return Summary{}, fmt.Errorf("evaluation: encode result for %q: %w", record.CandidateID, err)
		}
		if options.Progress != nil {
			options.Progress(index+1, len(records), result)
		}
	}
	summary.DurationMS = time.Since(started).Milliseconds()
	return summary, nil
}

func evaluateOne(parent context.Context, record Record, factory ReviewerFactory, timeout time.Duration) (result Result) {
	started := time.Now()
	result = Result{
		SchemaVersion: ResultSchemaVersion, CandidateID: record.CandidateID,
		AdvisoryID: record.AdvisoryID, Repository: record.Repository, Rank: record.Selection.Rank,
		BaseSHA: record.BaseSHA(), HeadSHA: record.FixCommit, Status: "failed",
	}
	defer func() { result.DurationMS = time.Since(started).Milliseconds() }()

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	diff, err := MaterializeDiff(record)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	reviewerImpl, err := factory(ctx, record)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	report, err := review.NewService(reviewerImpl).Review(ctx, review.Request{
		Repository: record.Repository, BaseSHA: record.BaseSHA(), HeadSHA: record.FixCommit,
		Language: "go", Diff: diff,
	})
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Status = "completed"
	result.Report = &report
	return result
}

func validateRecord(record Record) error {
	if record.SchemaVersion != "v1" {
		return fmt.Errorf("candidate %q has unsupported schema_version %q", record.CandidateID, record.SchemaVersion)
	}
	if strings.TrimSpace(record.CandidateID) == "" || strings.TrimSpace(record.Repository) == "" ||
		strings.TrimSpace(record.FixCommit) == "" {
		return fmt.Errorf("candidate_id, repository and fix_commit are required")
	}
	if len(record.Commit.Parents) != 1 || strings.TrimSpace(record.Commit.Parents[0].SHA) == "" {
		return fmt.Errorf("candidate %q must have exactly one parent", record.CandidateID)
	}
	if len(record.Commit.Files) == 0 {
		return fmt.Errorf("candidate %q has no changed files", record.CandidateID)
	}
	return nil
}

func patchPaths(file FilePatch) (oldPath, newPath string, err error) {
	if strings.TrimSpace(file.Filename) == "" {
		return "", "", fmt.Errorf("evaluation: changed file has no filename")
	}
	switch file.Status {
	case "added":
		return "", file.Filename, nil
	case "removed":
		return file.Filename, "", nil
	case "renamed":
		if strings.TrimSpace(file.PreviousFilename) == "" {
			return "", "", fmt.Errorf("evaluation: renamed file %q has no previous_filename", file.Filename)
		}
		return file.PreviousFilename, file.Filename, nil
	case "modified", "changed", "copied":
		return file.Filename, file.Filename, nil
	default:
		return "", "", fmt.Errorf("evaluation: unsupported file status %q for %q", file.Status, file.Filename)
	}
}

func formatPath(path string) string {
	if strings.ContainsAny(path, " \t\n\r\"") {
		return strconv.Quote(path)
	}
	return path
}

func addRisk(counts *RiskCounts, risk string) {
	switch risk {
	case "critical":
		counts.Critical++
	case "high":
		counts.High++
	case "medium":
		counts.Medium++
	case "low":
		counts.Low++
	default:
		counts.None++
	}
}
