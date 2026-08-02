package agentreview

import (
	"context"
	"fmt"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/wen/opentalon/internal/tool/repository"
)

const (
	readRepositoryFileToolName  = "read_repository_file"
	searchRepositoryToolName    = "search_repository_symbol"
	listRepositoryFilesToolName = "list_repository_files"
)

// readRepositoryFileInput 是 read_repository_file 暴露给模型的参数。
// revision 只能选择创建 Reader 时绑定的 base 或 head，不能传入任意 Git Revision。
type readRepositoryFileInput struct {
	Revision  repository.Revision `json:"revision" jsonschema:"required,description=要读取的版本，只能是 base 或 head,enum=base,enum=head"`
	Path      string              `json:"path" jsonschema:"required,description=仓库内的相对文件路径"`
	StartLine int                 `json:"start_line" jsonschema:"required,description=起始行号，从 1 开始"`
	EndLine   int                 `json:"end_line" jsonschema:"required,description=结束行号，包含该行"`
}

// searchRepositorySymbolInput 是固定字符串符号搜索的参数，不允许模型传入正则表达式。
type searchRepositorySymbolInput struct {
	Revision repository.Revision `json:"revision" jsonschema:"required,description=要搜索的版本，只能是 base 或 head,enum=base,enum=head"`
	Symbol   string              `json:"symbol" jsonschema:"required,description=要搜索的 Go 标识符或限定名，例如 ParseConfig 或 config.Parse"`
}

// listRepositoryFilesInput 是仓库文件列表工具的参数。directory 为空时表示仓库根目录。
type listRepositoryFilesInput struct {
	Revision  repository.Revision `json:"revision" jsonschema:"required,description=要列出文件的版本，只能是 base 或 head,enum=base,enum=head"`
	Directory string              `json:"directory,omitempty" jsonschema:"description=仓库内的相对目录；省略时列出仓库根目录"`
}

// NewRepositoryTools 将请求级只读 Reader 包装为 Eino 可执行工具。
// 这些工具不会写入 Talon 的全局注册表，调用方应为每次 Review 显式创建并注入它们。
func NewRepositoryTools(reader repository.Reader) ([]einotool.InvokableTool, error) {
	if reader == nil {
		return nil, fmt.Errorf("agentreview: repository reader is required")
	}

	readFile, err := utils.InferTool(
		readRepositoryFileToolName,
		"读取本次代码审查所绑定的 base 或 head Git 版本中的文本文件片段，并返回稳定行号。不能访问工作区、.git 或任意 Revision。",
		func(ctx context.Context, input readRepositoryFileInput) (repository.FileSnippet, error) {
			return reader.ReadFile(ctx, input.Revision, input.Path, input.StartLine, input.EndLine)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("agentreview: build %s tool: %w", readRepositoryFileToolName, err)
	}

	searchSymbol, err := utils.InferTool(
		searchRepositoryToolName,
		"在本次代码审查所绑定的 base 或 head 版本的 Go 文件中按固定字符串搜索标识符，返回文件、行号和代码行。",
		func(ctx context.Context, input searchRepositorySymbolInput) ([]repository.SearchResult, error) {
			return reader.SearchSymbol(ctx, input.Revision, input.Symbol)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("agentreview: build %s tool: %w", searchRepositoryToolName, err)
	}

	listFiles, err := utils.InferTool(
		listRepositoryFilesToolName,
		"列出本次代码审查所绑定的 base 或 head 版本中指定目录下的仓库文件，用于发现需要进一步读取的代码。",
		func(ctx context.Context, input listRepositoryFilesInput) ([]string, error) {
			return reader.ListFiles(ctx, input.Revision, input.Directory)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("agentreview: build %s tool: %w", listRepositoryFilesToolName, err)
	}

	return []einotool.InvokableTool{readFile, searchSymbol, listFiles}, nil
}
