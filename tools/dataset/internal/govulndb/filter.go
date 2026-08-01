package govulndb

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const sourceLicense = "CC-BY-4.0"

var gitCommitPattern = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

// FilterDirectory 离线读取 inputDir 下的 OSV JSON，过滤后以确定性顺序原子写入 JSONL。
// 输出不包含生成时间，因此相同原始快照和程序版本会产生相同的文件哈希。
func FilterDirectory(ctx context.Context, inputDir, outputPath string) (Stats, error) {
	if strings.TrimSpace(inputDir) == "" {
		return Stats{}, fmt.Errorf("govulndb: input directory is required")
	}
	if strings.TrimSpace(outputPath) == "" {
		return Stats{}, fmt.Errorf("govulndb: output path is required")
	}

	entries, err := readEntries(ctx, inputDir)
	if err != nil {
		return Stats{}, err
	}

	stats := Stats{TotalEntries: len(entries), OutputPath: filepath.ToSlash(filepath.Clean(outputPath))}
	candidates := make([]Candidate, 0)
	for _, item := range entries {
		if err := ctx.Err(); err != nil {
			return Stats{}, err
		}
		entry := item.entry
		switch {
		case entry.Withdrawn != "":
			stats.WithdrawnEntries++
			continue
		case reviewStatus(entry) != "REVIEWED":
			stats.UnreviewedEntries++
			continue
		}

		modules := externalModules(entry)
		if len(modules) == 0 {
			stats.NoExternalModuleEntries++
			continue
		}
		fixes := githubCommitFixes(entry)
		if len(fixes) == 0 {
			stats.NoGitHubCommitFixEntries++
			continue
		}

		stats.EligibleEntries++
		for _, fix := range fixes {
			candidates = append(candidates, buildCandidate(item, modules, fix))
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].CandidateID < candidates[j].CandidateID
	})
	stats.CandidateRecords = len(candidates)
	outputHash, err := writeJSONLAtomic(outputPath, candidates)
	if err != nil {
		return Stats{}, err
	}
	stats.OutputSHA256 = outputHash
	return stats, nil
}

type sourceEntry struct {
	entry      osvEntry
	sourceFile string
	sourceHash string
}

func readEntries(ctx context.Context, inputDir string) ([]sourceEntry, error) {
	info, err := os.Stat(inputDir)
	if err != nil {
		return nil, fmt.Errorf("govulndb: stat input directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("govulndb: input %q is not a directory", inputDir)
	}

	root := filepath.Clean(inputDir)
	sourceRoot := filepath.Dir(root)
	entries := make([]sourceEntry, 0)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		var record osvEntry
		if err := json.Unmarshal(data, &record); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
		if strings.TrimSpace(record.ID) == "" {
			return fmt.Errorf("decode %s: missing vulnerability id", path)
		}
		relative, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return fmt.Errorf("resolve source path %s: %w", path, err)
		}
		digest := sha256.Sum256(data)
		entries = append(entries, sourceEntry{
			entry: record, sourceFile: filepath.ToSlash(relative), sourceHash: hex.EncodeToString(digest[:]),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("govulndb: walk input directory: %w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("govulndb: no JSON records found in %q", inputDir)
	}
	return entries, nil
}

func reviewStatus(entry osvEntry) string {
	status := strings.ToUpper(strings.TrimSpace(entry.DatabaseSpecific.ReviewStatus))
	// Go 官方约定：review_status 缺失时应视为 REVIEWED。
	if status == "" {
		return "REVIEWED"
	}
	return status
}

func externalModules(entry osvEntry) []string {
	modules := make([]string, 0)
	for _, affected := range entry.Affected {
		name := strings.TrimSpace(affected.Package.Name)
		if !strings.EqualFold(affected.Package.Ecosystem, "Go") || name == "" || name == "stdlib" || name == "toolchain" {
			continue
		}
		modules = append(modules, name)
	}
	return uniqueSorted(modules)
}

func githubCommitFixes(entry osvEntry) []fixReference {
	byKey := make(map[string]fixReference)
	for _, reference := range entry.References {
		if !strings.EqualFold(reference.Type, "FIX") {
			continue
		}
		fix, ok := parseGitHubCommitURL(reference.URL)
		if !ok {
			continue
		}
		key := strings.ToLower(fix.Repository + "@" + fix.Commit)
		byKey[key] = fix
	}
	fixes := make([]fixReference, 0, len(byKey))
	for _, fix := range byKey {
		fixes = append(fixes, fix)
	}
	sort.Slice(fixes, func(i, j int) bool {
		if fixes[i].Repository != fixes[j].Repository {
			return fixes[i].Repository < fixes[j].Repository
		}
		return fixes[i].Commit < fixes[j].Commit
	})
	return fixes
}

func parseGitHubCommitURL(raw string) (fixReference, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Hostname(), "github.com") {
		return fixReference{}, false
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] != "commit" {
		return fixReference{}, false
	}
	owner, err := url.PathUnescape(parts[0])
	if err != nil {
		return fixReference{}, false
	}
	repository, err := url.PathUnescape(parts[1])
	if err != nil {
		return fixReference{}, false
	}
	commit, err := url.PathUnescape(parts[3])
	if err != nil || !gitCommitPattern.MatchString(commit) {
		return fixReference{}, false
	}
	return fixReference{
		URL: raw, Repository: owner + "/" + strings.TrimSuffix(repository, ".git"), Commit: strings.ToLower(commit),
	}, true
}

func buildCandidate(item sourceEntry, modules []string, fix fixReference) Candidate {
	entry := item.entry
	aliases := uniqueSorted(entry.Aliases)
	advisoryURLs := make([]string, 0)
	importsByPath := make(map[string]AffectedImport)
	for _, reference := range entry.References {
		if strings.EqualFold(reference.Type, "ADVISORY") && strings.TrimSpace(reference.URL) != "" {
			advisoryURLs = append(advisoryURLs, reference.URL)
		}
	}
	if entry.DatabaseSpecific.URL != "" {
		advisoryURLs = append(advisoryURLs, entry.DatabaseSpecific.URL)
	}
	for _, affected := range entry.Affected {
		for _, affectedImport := range affected.EcosystemSpecific.Imports {
			if affectedImport.Path == "" {
				continue
			}
			current := importsByPath[affectedImport.Path]
			current.Path = affectedImport.Path
			current.Symbols = uniqueSorted(append(current.Symbols, affectedImport.Symbols...))
			current.GOOS = uniqueSorted(append(current.GOOS, affectedImport.GOOS...))
			current.GOARCH = uniqueSorted(append(current.GOARCH, affectedImport.GOARCH...))
			importsByPath[affectedImport.Path] = current
		}
	}
	imports := make([]AffectedImport, 0, len(importsByPath))
	for _, affectedImport := range importsByPath {
		imports = append(imports, affectedImport)
	}
	sort.Slice(imports, func(i, j int) bool { return imports[i].Path < imports[j].Path })

	commitPrefix := fix.Commit
	if len(commitPrefix) > 12 {
		commitPrefix = commitPrefix[:12]
	}
	return Candidate{
		SchemaVersion: CandidateSchemaVersion,
		CandidateID:   entry.ID + "@" + commitPrefix, AdvisoryID: entry.ID,
		Aliases: aliases, Summary: entry.Summary, Details: entry.Details,
		Published: entry.Published, Modified: entry.Modified,
		Modules: append([]string(nil), modules...), AffectedImports: imports,
		AdvisoryURLs: uniqueSorted(advisoryURLs),
		Repository:   fix.Repository, FixCommit: fix.Commit, FixURL: fix.URL,
		ReviewStatus: "REVIEWED", SourceFile: item.sourceFile,
		SourceSHA256: item.sourceHash, SourceLicense: sourceLicense,
	}
}

func uniqueSorted(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func writeJSONLAtomic(outputPath string, candidates []Candidate) (string, error) {
	directory := filepath.Dir(outputPath)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", fmt.Errorf("govulndb: create output directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".candidates-*.tmp")
	if err != nil {
		return "", fmt.Errorf("govulndb: create temporary output: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	hasher := sha256.New()
	buffered := bufio.NewWriter(io.MultiWriter(temporary, hasher))
	encoder := json.NewEncoder(buffered)
	encoder.SetEscapeHTML(false)
	for _, candidate := range candidates {
		if err := encoder.Encode(candidate); err != nil {
			temporary.Close()
			return "", fmt.Errorf("govulndb: encode candidate: %w", err)
		}
	}
	if err := buffered.Flush(); err != nil {
		temporary.Close()
		return "", fmt.Errorf("govulndb: flush output: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return "", fmt.Errorf("govulndb: sync output: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("govulndb: close output: %w", err)
	}
	if err := os.Rename(temporaryPath, outputPath); err != nil {
		return "", fmt.Errorf("govulndb: replace output: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
