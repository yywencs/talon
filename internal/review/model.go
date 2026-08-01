// Package review 定义代码审查旁路的领域模型、解析流程和 Reviewer 扩展接口。
// 该包不依赖主 Agent Session，因此可以独立测试，也不会改变现有执行主链路。
package review

import "context"

// SchemaVersion 标识审查报告的数据结构版本，供 CLI、评测器和后续 API 做兼容判断。
const SchemaVersion = "v1"

// Severity 表示 Finding 的风险等级。
type Severity string

const (
	// SeverityCritical 表示需要立即阻断合并的致命风险。
	SeverityCritical Severity = "critical"
	// SeverityHigh 表示应在合并前修复的高风险问题。
	SeverityHigh Severity = "high"
	// SeverityMedium 表示需要开发者确认和处理的中风险问题。
	SeverityMedium Severity = "medium"
	// SeverityLow 表示影响有限的低风险问题。
	SeverityLow Severity = "low"
)

// Request 是所有代码审查实现共享的稳定输入契约。
// Diff 必须是 unified diff，可以来自本地 git、文件或远端 PR API。
type Request struct {
	Repository  string `json:"repository,omitempty"`
	PullRequest int    `json:"pull_request,omitempty"`
	BaseSHA     string `json:"base_sha,omitempty"`
	HeadSHA     string `json:"head_sha,omitempty"`
	Language    string `json:"language,omitempty"`
	Diff        string `json:"-"`
}

// LineKind 表示 unified diff 中一行的变更类型。
type LineKind string

const (
	// LineContext 表示新旧文件中都存在的上下文行。
	LineContext LineKind = "context"
	// LineAdded 表示仅在新文件中出现的新增行。
	LineAdded LineKind = "added"
	// LineRemoved 表示仅在旧文件中出现的删除行。
	LineRemoved LineKind = "removed"
)

// ChangedLine 保存一行变更及其在新旧文件中的真实行号。
// 对新增行 OldLine 为零，对删除行 NewLine 为零。
type ChangedLine struct {
	Kind    LineKind `json:"kind"`
	OldLine int      `json:"old_line,omitempty"`
	NewLine int      `json:"new_line,omitempty"`
	Content string   `json:"content"`
}

// Hunk 表示 unified diff 中一个以 @@ 开头的连续变更区块。
type Hunk struct {
	Header   string        `json:"header"`
	OldStart int           `json:"old_start"`
	OldCount int           `json:"old_count"`
	NewStart int           `json:"new_start"`
	NewCount int           `json:"new_count"`
	Lines    []ChangedLine `json:"lines"`
}

// FileStatus 表示文件级别的新增、删除或修改状态。
type FileStatus string

const (
	// FileModified 表示文件在新旧版本中都存在。
	FileModified FileStatus = "modified"
	// FileAdded 表示文件仅存在于新版本中。
	FileAdded FileStatus = "added"
	// FileDeleted 表示文件仅存在于旧版本中。
	FileDeleted FileStatus = "deleted"
)

// ChangedFile 保存单个文件的路径、状态和全部变更区块。
type ChangedFile struct {
	OldPath string     `json:"old_path,omitempty"`
	NewPath string     `json:"new_path,omitempty"`
	Status  FileStatus `json:"status"`
	Hunks   []Hunk     `json:"hunks"`
}

// Path 返回审查结果应关联的文件路径；删除文件没有新路径，因此回退到旧路径。
func (f ChangedFile) Path() string {
	if f.NewPath != "" {
		return f.NewPath
	}
	return f.OldPath
}

// Finding 是 Reviewer 输出的标准问题结构，同时携带定位、证据、修复及验证建议。
type Finding struct {
	RuleID      string   `json:"rule_id"`
	CWE         string   `json:"cwe,omitempty"`
	Severity    Severity `json:"severity"`
	Title       string   `json:"title"`
	Explanation string   `json:"explanation"`
	Path        string   `json:"path"`
	StartLine   int      `json:"start_line"`
	EndLine     int      `json:"end_line"`
	Evidence    string   `json:"evidence"`
	Fix         string   `json:"fix,omitempty"`
	Test        string   `json:"test,omitempty"`
	Confidence  float64  `json:"confidence"`
}

// Summary 按风险等级聚合审查结果，避免调用方重复计算。
type Summary struct {
	Total    int `json:"total"`
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
}

// Report 是一次代码审查的版本化输出，可直接序列化为 JSON 或交给评测器消费。
type Report struct {
	SchemaVersion string    `json:"schema_version"`
	Repository    string    `json:"repository,omitempty"`
	PullRequest   int       `json:"pull_request,omitempty"`
	BaseSHA       string    `json:"base_sha,omitempty"`
	HeadSHA       string    `json:"head_sha,omitempty"`
	Risk          string    `json:"risk"`
	FilesReviewed int       `json:"files_reviewed"`
	Reviewer      string    `json:"reviewer"`
	Summary       Summary   `json:"summary"`
	Findings      []Finding `json:"findings"`
}

// Reviewer 是代码审查策略的扩展点，刻意与 Agent Runtime 解耦。
// 确定性规则、LLM 或多 Agent Reviewer 都可以复用同一输入输出契约。
type Reviewer interface {
	Name() string
	Review(ctx context.Context, request Request, files []ChangedFile) ([]Finding, error)
}
