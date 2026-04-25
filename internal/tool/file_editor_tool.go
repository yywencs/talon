package tool

import (
	"context"
	"fmt"

	fileeditor "github.com/wen/opentalon/internal/tool/file_editor"
)

const fileEditorToolDescription = "# 纯文本文件编辑工具\n" +
	"用于查看、新建、编辑和撤销纯文本类文件。\n\n" +
	"## 核心能力\n" +
	"- 支持 `view`、`create`、`str_replace`、`insert`、`undo_edit` 五类指令\n" +
	"- 工具状态在单次指令调用和多轮对话之间持久保留\n" +
	"- 当 `path` 指向文件时，`view` 返回带行号的文本预览；当 `path` 指向目录时，`view` 返回最多两层的非隐藏文件和子目录\n" +
	"- 已有同名文件时，`create` 必须失败\n" +
	"- 输出过长时会自动截断，并追加 `...[truncated]`\n" +
	"- 仅支持纯文本类文件的创建与编辑；二进制文件和不可编辑编码文件只能查看，不能编辑\n\n" +
	"## 使用前置规则\n" +
	"1. 编辑前先执行 `view` 查看目标文件内容和上下文\n" +
	"2. 创建文件前先确认父目录存在且路径正确\n" +
	"3. 所有文件路径必须是绝对路径，以 `/` 开头\n\n" +
	"## 编辑规则\n" +
	"- 编辑结果必须保持语法完整、结构完整、可继续使用\n" +
	"- 禁止生成残缺、破损或不可解析的文本内容\n" +
	"- 对同一文件的连续修改，优先合并为一次指令提交\n\n" +
	"## `str_replace` 强约束\n" +
	"1. `old_str` 必须能与文件中的连续文本唯一匹配\n" +
	"2. `old_str` 匹配到 0 处时，替换必须失败\n" +
	"3. `old_str` 匹配到多处时，替换必须失败\n" +
	"4. `new_str` 必须作为新的完整替换内容写回\n" +
	"5. 为降低重复匹配风险，调用时优先提供前后 3 到 5 行上下文\n\n" +
	"## `insert` 规则\n" +
	"1. `insert_line` 必须从 1 开始\n" +
	"2. 插入位置必须在允许范围内\n" +
	"3. 插入成功后会返回目标文件路径和插入附近的上下文预览\n\n" +
	"## `undo_edit` 规则\n" +
	"1. 仅撤销最近一次已记录的编辑\n" +
	"2. 没有历史记录时必须失败\n" +
	"3. 撤销后会返回写回结果的内容预览"

func fileEditorExecutor(ctx context.Context, action fileeditor.FileEditorAction) *fileeditor.FileEditorObservation {
	editor, err := fileeditor.DefaultFileEditor()
	if err != nil {
		return fileeditor.NewErrorObservation(action.Command, action.Path, fmt.Errorf("create default file editor: %w", err))
	}
	return editor.Execute(ctx, action)
}

func newFileEditorTool() *BaseTool[fileeditor.FileEditorAction, *fileeditor.FileEditorObservation] {
	return &BaseTool[fileeditor.FileEditorAction, *fileeditor.FileEditorObservation]{
		ToolName: "file_editor",
		ToolDesc: fileEditorToolDescription,
		Executor: fileEditorExecutor,
	}
}

func init() {
	Register("file_editor", func(ctx context.Context) Tool {
		return newFileEditorTool()
	})
}
