// Package govulndb 将 Go Vulnerability Database 的 OSV 记录转换为代码审查候选数据。
// 原始 OSV 文件始终只读；仓库下载、Diff 生成和人工标注由后续离线阶段完成。
package govulndb

// CandidateSchemaVersion 是候选 JSONL 的数据结构版本。
const CandidateSchemaVersion = "v1"

// AffectedImport 保留漏洞影响的 Go 包、符号及平台范围。
type AffectedImport struct {
	Path    string   `json:"path"`
	Symbols []string `json:"symbols,omitempty"`
	GOOS    []string `json:"goos,omitempty"`
	GOARCH  []string `json:"goarch,omitempty"`
}

// Candidate 表示一条可以继续获取 Fix Commit 和生成 Diff 的已审核漏洞候选。
// 同一 Advisory 可能包含多个 Fix Commit，因此 CandidateID 会包含 commit 前缀。
type Candidate struct {
	SchemaVersion   string           `json:"schema_version"`
	CandidateID     string           `json:"candidate_id"`
	AdvisoryID      string           `json:"advisory_id"`
	Aliases         []string         `json:"aliases,omitempty"`
	Summary         string           `json:"summary"`
	Details         string           `json:"details,omitempty"`
	Published       string           `json:"published,omitempty"`
	Modified        string           `json:"modified,omitempty"`
	Modules         []string         `json:"modules"`
	AffectedImports []AffectedImport `json:"affected_imports,omitempty"`
	AdvisoryURLs    []string         `json:"advisory_urls,omitempty"`
	Repository      string           `json:"repository"`
	FixCommit       string           `json:"fix_commit"`
	FixURL          string           `json:"fix_url"`
	ReviewStatus    string           `json:"review_status"`
	SourceFile      string           `json:"source_file"`
	SourceSHA256    string           `json:"source_sha256"`
	SourceLicense   string           `json:"source_license"`
}

// Stats 记录过滤过程中每个互斥阶段的数量，并提供最终输出摘要。
type Stats struct {
	TotalEntries             int    `json:"total_entries"`
	WithdrawnEntries         int    `json:"withdrawn_entries"`
	UnreviewedEntries        int    `json:"unreviewed_entries"`
	NoExternalModuleEntries  int    `json:"no_external_module_entries"`
	NoGitHubCommitFixEntries int    `json:"no_github_commit_fix_entries"`
	EligibleEntries          int    `json:"eligible_entries"`
	CandidateRecords         int    `json:"candidate_records"`
	OutputPath               string `json:"output_path"`
	OutputSHA256             string `json:"output_sha256"`
}

type osvEntry struct {
	SchemaVersion string   `json:"schema_version"`
	ID            string   `json:"id"`
	Modified      string   `json:"modified"`
	Published     string   `json:"published"`
	Withdrawn     string   `json:"withdrawn"`
	Aliases       []string `json:"aliases"`
	Summary       string   `json:"summary"`
	Details       string   `json:"details"`
	Affected      []struct {
		Package struct {
			Name      string `json:"name"`
			Ecosystem string `json:"ecosystem"`
		} `json:"package"`
		EcosystemSpecific struct {
			Imports []AffectedImport `json:"imports"`
		} `json:"ecosystem_specific"`
	} `json:"affected"`
	References []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"references"`
	DatabaseSpecific struct {
		URL          string `json:"url"`
		ReviewStatus string `json:"review_status"`
	} `json:"database_specific"`
}

type fixReference struct {
	URL        string
	Repository string
	Commit     string
}
