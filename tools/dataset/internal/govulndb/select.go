package govulndb

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const SelectionSchemaVersion = "v1"

const (
	CategoryInjection  = "injection"
	CategoryAuth       = "auth"
	CategoryNetwork    = "network"
	CategoryFilesystem = "filesystem"
	CategoryCrypto     = "crypto"
	CategoryDoSMemory  = "dos_memory"
	CategoryOther      = "other"
)

var categoryOrder = []string{
	CategoryInjection,
	CategoryAuth,
	CategoryNetwork,
	CategoryFilesystem,
	CategoryCrypto,
	CategoryDoSMemory,
	CategoryOther,
}

// SelectOptions 控制首批评测样本的数量和稳定随机种子。
// 同一份输入、Size 和 Seed 必须得到字节级一致的输出。
type SelectOptions struct {
	InputPath  string
	OutputPath string
	Size       int
	Seed       string
}

// SelectionEvidence 解释一条记录为何满足“可以直接做代码审查评测”的硬条件。
type SelectionEvidence struct {
	MatchedSymbols    []string `json:"matched_symbols"`
	ChangedTestFiles  []string `json:"changed_test_files"`
	ReviewableGoFiles []string `json:"reviewable_go_files"`
	ChangedFiles      int      `json:"changed_files"`
	ChangedLines      int      `json:"changed_lines"`
}

// Selection 保存可审计的分类、评分和选择顺序，不依赖运行时间等易变信息。
type Selection struct {
	SchemaVersion string            `json:"schema_version"`
	Rank          int               `json:"rank"`
	Category      string            `json:"category"`
	Year          string            `json:"year"`
	Score         int               `json:"score"`
	Seed          string            `json:"seed"`
	Evidence      SelectionEvidence `json:"evidence"`
}

// SelectedCandidate 保留完整 Commit Patch，后续可以直接转成 Review 输入。
type SelectedCandidate struct {
	EnrichedCandidate
	Selection Selection `json:"selection"`
}

// SelectStats 汇总硬筛选损耗、最终分布和输出文件指纹。
type SelectStats struct {
	InputRecords        int            `json:"input_records"`
	StrictCandidates    int            `json:"strict_candidates"`
	SelectedRecords     int            `json:"selected_records"`
	HardRejectionCounts map[string]int `json:"hard_rejection_counts"`
	CategoryCounts      map[string]int `json:"category_counts"`
	YearCounts          map[string]int `json:"year_counts"`
	UniqueRepositories  int            `json:"unique_repositories"`
	OutputPath          string         `json:"output_path"`
	OutputSHA256        string         `json:"output_sha256"`
}

type selectableCandidate struct {
	record   EnrichedCandidate
	category string
	year     string
	score    int
	tie      string
	evidence SelectionEvidence
}

// SelectFile 对 enriched JSONL 执行严格证据筛选、分层抽样和仓库去重。
func SelectFile(options SelectOptions) (SelectStats, error) {
	if strings.TrimSpace(options.InputPath) == "" || strings.TrimSpace(options.OutputPath) == "" {
		return SelectStats{}, fmt.Errorf("govulndb: selection input and output paths are required")
	}
	if options.Size <= 0 {
		return SelectStats{}, fmt.Errorf("govulndb: selection size must be greater than zero")
	}
	if strings.TrimSpace(options.Seed) == "" {
		return SelectStats{}, fmt.Errorf("govulndb: selection seed is required")
	}

	records, err := readEnrichedJSONL(options.InputPath)
	if err != nil {
		return SelectStats{}, err
	}
	stats := SelectStats{
		InputRecords: len(records), OutputPath: filepath.ToSlash(filepath.Clean(options.OutputPath)),
		HardRejectionCounts: make(map[string]int), CategoryCounts: make(map[string]int), YearCounts: make(map[string]int),
	}
	selectable := make([]selectableCandidate, 0, len(records))
	for _, record := range records {
		candidate, reasons := assessForSelection(record, options.Seed)
		if len(reasons) != 0 {
			for _, reason := range reasons {
				stats.HardRejectionCounts[reason]++
			}
			continue
		}
		selectable = append(selectable, candidate)
	}
	stats.StrictCandidates = len(selectable)
	if len(selectable) < options.Size {
		return SelectStats{}, fmt.Errorf("govulndb: only %d strict candidates are available for requested size %d", len(selectable), options.Size)
	}

	selected, err := stratifiedSelect(selectable, options.Size)
	if err != nil {
		return SelectStats{}, err
	}
	output := make([]SelectedCandidate, 0, len(selected))
	repositories := make(map[string]struct{})
	for index, candidate := range selected {
		selection := Selection{
			SchemaVersion: SelectionSchemaVersion,
			Rank:          index + 1,
			Category:      candidate.category,
			Year:          candidate.year,
			Score:         candidate.score,
			Seed:          options.Seed,
			Evidence:      candidate.evidence,
		}
		output = append(output, SelectedCandidate{EnrichedCandidate: candidate.record, Selection: selection})
		stats.CategoryCounts[candidate.category]++
		stats.YearCounts[candidate.year]++
		repositories[candidate.record.Repository] = struct{}{}
	}
	stats.SelectedRecords = len(output)
	stats.UniqueRepositories = len(repositories)
	outputHash, err := writeSelectedJSONLAtomic(options.OutputPath, output)
	if err != nil {
		return SelectStats{}, err
	}
	stats.OutputSHA256 = outputHash
	return stats, nil
}

func assessForSelection(record EnrichedCandidate, seed string) (selectableCandidate, []string) {
	reasons := make([]string, 0)
	if record.Status != StatusMaterializable || record.Commit == nil {
		reasons = append(reasons, "not_materializable")
		return selectableCandidate{}, reasons
	}
	if record.Commit.Stats.Total < 5 || record.Commit.Stats.Total > 200 {
		reasons = append(reasons, "changed_lines_out_of_range")
	}
	if len(record.Commit.Files) == 0 || len(record.Commit.Files) > 4 {
		reasons = append(reasons, "changed_files_out_of_range")
	}
	if len(record.ReviewableGoFiles) == 0 || len(record.ReviewableGoFiles) > 3 {
		reasons = append(reasons, "reviewable_go_files_out_of_range")
	}

	symbols := affectedSymbols(record.AffectedImports)
	if len(symbols) == 0 {
		reasons = append(reasons, "affected_symbols_missing")
	}
	patch := combinedPatch(record)
	matched := matchedAffectedSymbols(symbols, patch)
	if len(symbols) > 0 && len(matched) == 0 {
		reasons = append(reasons, "affected_symbol_not_in_patch")
	}
	testFiles := changedTestFiles(record)
	if len(testFiles) == 0 {
		reasons = append(reasons, "changed_test_file_missing")
	}
	if len(reasons) != 0 {
		return selectableCandidate{}, reasons
	}

	evidence := SelectionEvidence{
		MatchedSymbols: matched, ChangedTestFiles: testFiles,
		ReviewableGoFiles: append([]string(nil), record.ReviewableGoFiles...),
		ChangedFiles:      len(record.Commit.Files), ChangedLines: record.Commit.Stats.Total,
	}
	sort.Strings(evidence.ReviewableGoFiles)
	score := 7 // 符号命中 4 分、测试改动 3 分；两项都是首批数据的硬条件。
	if record.Commit.Stats.Total <= 80 {
		score += 2
	} else {
		score++
	}
	if len(record.ReviewableGoFiles) == 1 {
		score += 2
	}
	if len(record.Commit.Files) <= 3 {
		score++
	}
	if hasGHSAAlias(record.Aliases) {
		score++
	}
	return selectableCandidate{
		record: record, category: classifyVulnerability(record), year: publishedYear(record.Published),
		score: score, tie: stableTie(seed, record.CandidateID), evidence: evidence,
	}, nil
}

func stratifiedSelect(candidates []selectableCandidate, size int) ([]selectableCandidate, error) {
	byCategory := make(map[string][]selectableCandidate)
	for _, candidate := range candidates {
		byCategory[candidate.category] = append(byCategory[candidate.category], candidate)
	}
	for category := range byCategory {
		sortSelectable(byCategory[category])
	}
	all := append([]selectableCandidate(nil), candidates...)
	sortSelectable(all)

	quotas := selectionQuotas(size)
	yearCap := int(math.Ceil(float64(size) / 3.0))
	selected := make([]selectableCandidate, 0, size)
	usedCandidates := make(map[string]struct{})
	usedRepositories := make(map[string]struct{})
	usedAdvisories := make(map[string]struct{})
	yearCounts := make(map[string]int)
	tryAdd := func(candidate selectableCandidate, enforceYearCap bool) bool {
		if _, exists := usedCandidates[candidate.record.CandidateID]; exists {
			return false
		}
		if _, exists := usedRepositories[candidate.record.Repository]; exists {
			return false
		}
		if _, exists := usedAdvisories[candidate.record.AdvisoryID]; exists {
			return false
		}
		if enforceYearCap && yearCounts[candidate.year] >= yearCap {
			return false
		}
		selected = append(selected, candidate)
		usedCandidates[candidate.record.CandidateID] = struct{}{}
		usedRepositories[candidate.record.Repository] = struct{}{}
		usedAdvisories[candidate.record.AdvisoryID] = struct{}{}
		yearCounts[candidate.year]++
		return true
	}

	for _, category := range categoryOrder {
		added := 0
		for _, candidate := range byCategory[category] {
			if added >= quotas[category] {
				break
			}
			if tryAdd(candidate, true) {
				added++
			}
		}
	}
	for _, candidate := range all {
		if len(selected) == size {
			break
		}
		tryAdd(candidate, true)
	}
	// 年份只是多样性约束；若它阻碍填满数据，最后只放宽年份，仓库和 Advisory 仍不重复。
	for _, candidate := range all {
		if len(selected) == size {
			break
		}
		tryAdd(candidate, false)
	}
	if len(selected) != size {
		return nil, fmt.Errorf("govulndb: cannot select %d unique repository/advisory records; selected %d", size, len(selected))
	}
	return selected, nil
}

func selectionQuotas(size int) map[string]int {
	base := map[string]int{
		CategoryInjection: 2, CategoryAuth: 2, CategoryNetwork: 2, CategoryFilesystem: 2,
		CategoryCrypto: 2, CategoryDoSMemory: 3, CategoryOther: 2,
	}
	quotas := make(map[string]int, len(base))
	assigned := 0
	for _, category := range categoryOrder {
		quotas[category] = size * base[category] / 15
		assigned += quotas[category]
	}
	for index := 0; assigned < size; index++ {
		category := categoryOrder[index%len(categoryOrder)]
		quotas[category]++
		assigned++
	}
	return quotas
}

func sortSelectable(candidates []selectableCandidate) {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		if candidates[i].tie != candidates[j].tie {
			return candidates[i].tie < candidates[j].tie
		}
		return candidates[i].record.CandidateID < candidates[j].record.CandidateID
	})
}

func classifyVulnerability(record EnrichedCandidate) string {
	text := strings.ToLower(record.Summary + "\n" + record.Details)
	categories := []struct {
		name     string
		keywords []string
	}{
		{CategoryInjection, []string{"injection", "cross-site scripting", " xss", "sql query", "command execution", "log entry", "template escape"}},
		{CategoryFilesystem, []string{"path traversal", "directory traversal", "zip slip", "symlink", "arbitrary file", "path escape", "archive extraction"}},
		{CategoryNetwork, []string{"server-side request forgery", " ssrf", "request smuggling", "open redirect", "host header", "ip spoof", "header spoof", "proxy bypass"}},
		{CategoryCrypto, []string{"certificate", " tls", "signature", " jwt", "encryption", "cryptograph", "nonce", "key validation", "randomness"}},
		{CategoryAuth, []string{"authentication", "authorization", "access control", "privilege", "permission", "credential", "session", "bearer token", "auth bypass"}},
		{CategoryDoSMemory, []string{"denial of service", "resource exhaustion", "infinite loop", "out of bounds", "out-of-bounds", "integer overflow", "memory exhaustion", "cpu exhaustion", "decompression bomb", "panic"}},
	}
	for _, category := range categories {
		for _, keyword := range category.keywords {
			if strings.Contains(text, keyword) {
				return category.name
			}
		}
	}
	return CategoryOther
}

func affectedSymbols(imports []AffectedImport) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0)
	for _, affectedImport := range imports {
		for _, symbol := range affectedImport.Symbols {
			symbol = strings.TrimSpace(symbol)
			if symbol == "" {
				continue
			}
			if _, exists := seen[symbol]; exists {
				continue
			}
			seen[symbol] = struct{}{}
			result = append(result, symbol)
		}
	}
	sort.Strings(result)
	return result
}

func matchedAffectedSymbols(symbols []string, patch string) []string {
	matched := make([]string, 0)
	for _, symbol := range symbols {
		term := symbol
		if dot := strings.LastIndex(term, "."); dot >= 0 {
			term = term[dot+1:]
		}
		term = strings.Trim(term, "() *")
		if len(term) >= 3 && containsIdentifier(patch, term) {
			matched = append(matched, symbol)
		}
	}
	return matched
}

func containsIdentifier(text, identifier string) bool {
	for start := 0; ; {
		index := strings.Index(text[start:], identifier)
		if index < 0 {
			return false
		}
		index += start
		leftOK := index == 0 || !isIdentifierRune(rune(text[index-1]))
		end := index + len(identifier)
		rightOK := end == len(text) || !isIdentifierRune(rune(text[end]))
		if leftOK && rightOK {
			return true
		}
		start = index + len(identifier)
	}
}

func isIdentifierRune(value rune) bool {
	return value == '_' || unicode.IsLetter(value) || unicode.IsDigit(value)
}

func combinedPatch(record EnrichedCandidate) string {
	var builder strings.Builder
	for _, file := range record.Commit.Files {
		builder.WriteString(file.Patch)
		builder.WriteByte('\n')
	}
	return builder.String()
}

func changedTestFiles(record EnrichedCandidate) []string {
	result := make([]string, 0)
	for _, file := range record.Commit.Files {
		if strings.HasSuffix(filepath.ToSlash(file.Filename), "_test.go") {
			result = append(result, file.Filename)
		}
	}
	sort.Strings(result)
	return result
}

func hasGHSAAlias(aliases []string) bool {
	for _, alias := range aliases {
		if strings.HasPrefix(strings.ToUpper(alias), "GHSA-") {
			return true
		}
	}
	return false
}

func publishedYear(published string) string {
	if len(published) >= 4 {
		year := published[:4]
		for _, value := range year {
			if value < '0' || value > '9' {
				return "unknown"
			}
		}
		return year
	}
	return "unknown"
}

func stableTie(seed, candidateID string) string {
	digest := sha256.Sum256([]byte(seed + "\x00" + candidateID))
	return hex.EncodeToString(digest[:])
}

func readEnrichedJSONL(path string) ([]EnrichedCandidate, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("govulndb: open enriched candidates: %w", err)
	}
	defer file.Close()
	records := make([]EnrichedCandidate, 0)
	seen := make(map[string]struct{})
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var record EnrichedCandidate
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, fmt.Errorf("govulndb: decode enriched candidate line %d: %w", lineNumber, err)
		}
		if record.CandidateID == "" {
			return nil, fmt.Errorf("govulndb: enriched candidate line %d is missing candidate_id", lineNumber)
		}
		if _, exists := seen[record.CandidateID]; exists {
			return nil, fmt.Errorf("govulndb: duplicate enriched candidate id %q", record.CandidateID)
		}
		seen[record.CandidateID] = struct{}{}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("govulndb: scan enriched candidates: %w", err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("govulndb: no enriched candidates found in %q", path)
	}
	return records, nil
}

func writeSelectedJSONLAtomic(outputPath string, records []SelectedCandidate) (string, error) {
	directory := filepath.Dir(outputPath)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", fmt.Errorf("govulndb: create selection output directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".selected-*.tmp")
	if err != nil {
		return "", fmt.Errorf("govulndb: create selection output: %w", err)
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
			return "", fmt.Errorf("govulndb: encode selected record: %w", err)
		}
	}
	if err := buffered.Flush(); err != nil {
		temporary.Close()
		return "", fmt.Errorf("govulndb: flush selection output: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return "", fmt.Errorf("govulndb: sync selection output: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("govulndb: close selection output: %w", err)
	}
	if err := os.Rename(temporaryPath, outputPath); err != nil {
		return "", fmt.Errorf("govulndb: replace selection output: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
