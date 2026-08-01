package govulndb

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/wen/opentalon/tools/dataset/internal/githubapi"
)

const EnrichmentSchemaVersion = "v1"

const (
	StatusMaterializable = "materializable"
	StatusRejected       = "rejected"
	StatusFetchFailed    = "fetch_failed"
)

// CommitFetcher 抽象 GitHub 数据来源，便于使用本地假响应测试完整加工流程。
type CommitFetcher interface {
	GetCommit(ctx context.Context, repository, commit string) (githubapi.FetchResult, error)
}

// EnrichOptions 定义输入输出、并发和首轮候选规模限制。
type EnrichOptions struct {
	InputPath   string
	OutputPath  string
	Concurrency int
	Limit       int
	MaxFiles    int
	MaxChanges  int
	Progress    func(done, total int)
}

// EnrichedCandidate 在原始 Candidate 上补充不可变 Commit 元数据和筛选结论。
// Candidate 使用匿名嵌入，JSONL 仍保持扁平结构，便于 jq 和后续流水线消费。
type EnrichedCandidate struct {
	Candidate
	EnrichmentSchemaVersion string   `json:"enrichment_schema_version"`
	Status                  string   `json:"status"`
	RejectionReasons        []string `json:"rejection_reasons,omitempty"`
	FetchError              string   `json:"fetch_error,omitempty"`
	// CacheHit 只用于本次运行统计，不能写入数据集，否则缓存冷热会改变输出内容。
	CacheHit          bool              `json:"-"`
	FilesTruncated    bool              `json:"files_truncated"`
	ReviewableGoFiles []string          `json:"reviewable_go_files,omitempty"`
	Commit            *githubapi.Commit `json:"commit,omitempty"`
}

// EnrichStats 汇总本轮实际处理的 Candidate 数量及输出指纹。
type EnrichStats struct {
	InputCandidates       int    `json:"input_candidates"`
	ProcessedCandidates   int    `json:"processed_candidates"`
	MaterializableRecords int    `json:"materializable_records"`
	RejectedRecords       int    `json:"rejected_records"`
	FetchFailedRecords    int    `json:"fetch_failed_records"`
	CacheHits             int    `json:"cache_hits"`
	OutputPath            string `json:"output_path"`
	OutputSHA256          string `json:"output_sha256"`
}

type enrichmentOutcome struct {
	record EnrichedCandidate
}

// EnrichFile 并发获取 Commit 元数据、执行确定性筛选，并原子生成 enriched JSONL。
func EnrichFile(ctx context.Context, fetcher CommitFetcher, options EnrichOptions) (EnrichStats, error) {
	if fetcher == nil {
		return EnrichStats{}, fmt.Errorf("govulndb: commit fetcher is required")
	}
	if strings.TrimSpace(options.InputPath) == "" || strings.TrimSpace(options.OutputPath) == "" {
		return EnrichStats{}, fmt.Errorf("govulndb: input and output paths are required")
	}
	if options.Concurrency <= 0 {
		return EnrichStats{}, fmt.Errorf("govulndb: concurrency must be greater than zero")
	}
	if options.Limit < 0 || options.MaxFiles <= 0 || options.MaxChanges <= 0 {
		return EnrichStats{}, fmt.Errorf("govulndb: limit and size constraints are invalid")
	}

	candidates, err := readCandidateJSONL(options.InputPath)
	if err != nil {
		return EnrichStats{}, err
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].CandidateID < candidates[j].CandidateID })
	stats := EnrichStats{InputCandidates: len(candidates), OutputPath: filepath.ToSlash(filepath.Clean(options.OutputPath))}
	if options.Limit > 0 && len(candidates) > options.Limit {
		candidates = candidates[:options.Limit]
	}

	jobs := make(chan Candidate)
	results := make(chan enrichmentOutcome)
	var workers sync.WaitGroup
	workerCount := options.Concurrency
	if workerCount > len(candidates) {
		workerCount = len(candidates)
	}
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for candidate := range jobs {
				results <- enrichmentOutcome{record: enrichCandidate(ctx, fetcher, candidate, options)}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, candidate := range candidates {
			select {
			case <-ctx.Done():
				return
			case jobs <- candidate:
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()

	records := make([]EnrichedCandidate, 0, len(candidates))
	for outcome := range results {
		records = append(records, outcome.record)
		if options.Progress != nil {
			options.Progress(len(records), len(candidates))
		}
	}
	if err := ctx.Err(); err != nil {
		return EnrichStats{}, err
	}
	sort.Slice(records, func(i, j int) bool { return records[i].CandidateID < records[j].CandidateID })
	for _, record := range records {
		if record.CacheHit {
			stats.CacheHits++
		}
		switch record.Status {
		case StatusMaterializable:
			stats.MaterializableRecords++
		case StatusRejected:
			stats.RejectedRecords++
		case StatusFetchFailed:
			stats.FetchFailedRecords++
		}
	}
	stats.ProcessedCandidates = len(records)
	outputHash, err := writeEnrichedJSONLAtomic(options.OutputPath, records)
	if err != nil {
		return EnrichStats{}, err
	}
	stats.OutputSHA256 = outputHash
	return stats, nil
}

func enrichCandidate(ctx context.Context, fetcher CommitFetcher, candidate Candidate, options EnrichOptions) EnrichedCandidate {
	record := EnrichedCandidate{Candidate: candidate, EnrichmentSchemaVersion: EnrichmentSchemaVersion}
	result, err := fetcher.GetCommit(ctx, candidate.Repository, candidate.FixCommit)
	if err != nil {
		record.Status = StatusFetchFailed
		record.FetchError = err.Error()
		return record
	}
	record.CacheHit = result.CacheHit
	record.FilesTruncated = result.FilesTruncated
	record.Commit = &result.Commit

	reasons := make([]string, 0)
	if len(result.Commit.Parents) != 1 {
		reasons = append(reasons, "parent_count_not_one")
	}
	if result.FilesTruncated {
		reasons = append(reasons, "changed_files_truncated")
	}
	if len(result.Commit.Files) > options.MaxFiles {
		reasons = append(reasons, "too_many_changed_files")
	}
	if result.Commit.Stats.Total > options.MaxChanges {
		reasons = append(reasons, "too_many_changed_lines")
	}

	reviewable := make([]string, 0)
	patchUnavailable := false
	for _, file := range result.Commit.Files {
		if !isReviewableGoFile(file.Filename) {
			continue
		}
		reviewable = append(reviewable, file.Filename)
		if strings.TrimSpace(file.Patch) == "" {
			patchUnavailable = true
		}
	}
	sort.Strings(reviewable)
	record.ReviewableGoFiles = reviewable
	if len(reviewable) == 0 {
		reasons = append(reasons, "no_reviewable_go_file")
	} else if patchUnavailable {
		reasons = append(reasons, "go_patch_unavailable")
	}
	if len(reasons) > 0 {
		record.Status = StatusRejected
		record.RejectionReasons = reasons
		return record
	}
	record.Status = StatusMaterializable
	return record
}

func isReviewableGoFile(path string) bool {
	path = strings.TrimPrefix(filepath.ToSlash(path), "./")
	if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
		return false
	}
	segments := strings.Split(path, "/")
	for _, segment := range segments[:len(segments)-1] {
		if segment == "vendor" || segment == "third_party" || segment == "testdata" || segment == "generated" {
			return false
		}
	}
	name := segments[len(segments)-1]
	return !strings.HasSuffix(name, ".pb.go") && !strings.HasSuffix(name, "_generated.go") && !strings.HasPrefix(name, "zz_generated.")
}

func readCandidateJSONL(path string) ([]Candidate, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("govulndb: open candidates: %w", err)
	}
	defer file.Close()

	candidates := make([]Candidate, 0)
	seen := make(map[string]struct{})
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var candidate Candidate
		if err := json.Unmarshal(scanner.Bytes(), &candidate); err != nil {
			return nil, fmt.Errorf("govulndb: decode candidate line %d: %w", lineNumber, err)
		}
		if candidate.CandidateID == "" || candidate.Repository == "" || candidate.FixCommit == "" {
			return nil, fmt.Errorf("govulndb: candidate line %d is missing required fields", lineNumber)
		}
		if _, exists := seen[candidate.CandidateID]; exists {
			return nil, fmt.Errorf("govulndb: duplicate candidate id %q", candidate.CandidateID)
		}
		seen[candidate.CandidateID] = struct{}{}
		candidates = append(candidates, candidate)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("govulndb: scan candidates: %w", err)
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("govulndb: no candidates found in %q", path)
	}
	return candidates, nil
}

func writeEnrichedJSONLAtomic(outputPath string, records []EnrichedCandidate) (string, error) {
	directory := filepath.Dir(outputPath)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", fmt.Errorf("govulndb: create enriched output directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".enriched-*.tmp")
	if err != nil {
		return "", fmt.Errorf("govulndb: create enriched output: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	hasher := sha256.New()
	buffered := bufio.NewWriter(io.MultiWriter(temporary, hasher))
	encoder := json.NewEncoder(buffered)
	encoder.SetEscapeHTML(false)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			temporary.Close()
			return "", fmt.Errorf("govulndb: encode enriched record: %w", err)
		}
	}
	if err := buffered.Flush(); err != nil {
		temporary.Close()
		return "", fmt.Errorf("govulndb: flush enriched output: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return "", fmt.Errorf("govulndb: sync enriched output: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("govulndb: close enriched output: %w", err)
	}
	if err := os.Rename(temporaryPath, outputPath); err != nil {
		return "", fmt.Errorf("govulndb: replace enriched output: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
