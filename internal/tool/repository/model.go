// Package repository 提供面向 Agent 的受控只读 Git 仓库能力。
// 调用方只能选择预先绑定的 base/head 版本，不能向底层传入任意 Git Revision。
package repository

import "context"

// Revision 是 Agent 可以读取的有限版本集合。
type Revision string

const (
	RevisionBase Revision = "base"
	RevisionHead Revision = "head"
)

// FileSnippet 是带稳定行号的文本片段。
type FileSnippet struct {
	Revision  Revision `json:"revision"`
	Path      string   `json:"path"`
	StartLine int      `json:"start_line"`
	EndLine   int      `json:"end_line"`
	Content   string   `json:"content"`
}

// SearchResult 表示一次固定字符串搜索命中的代码行。
type SearchResult struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

// Reader 是 Agent 可使用的最小只读仓库能力。
type Reader interface {
	ReadFile(ctx context.Context, revision Revision, path string, startLine, endLine int) (FileSnippet, error)
	SearchSymbol(ctx context.Context, revision Revision, symbol string) ([]SearchResult, error)
	ListFiles(ctx context.Context, revision Revision, directory string) ([]string, error)
}

// Limits 防止一次工具调用把整个仓库或超大文件送入模型上下文。
type Limits struct {
	MaxFileBytes    int64
	MaxReadLines    int
	MaxSearchResult int
	MaxListedFiles  int
	MaxCommandBytes int64
}

func defaultLimits() Limits {
	return Limits{
		MaxFileBytes: 256 << 10, MaxReadLines: 400,
		MaxSearchResult: 30, MaxListedFiles: 200, MaxCommandBytes: 1 << 20,
	}
}
