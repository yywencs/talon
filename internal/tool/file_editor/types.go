package file_editor

import (
	"errors"
	"fmt"
	"strings"

	"github.com/wen/opentalon/internal/types"
)

// FileEditorCommand 表示文件编辑工具支持的子命令。
type FileEditorCommand string

const (
	// FileEditorCommandView 表示查看文件内容。
	FileEditorCommandView FileEditorCommand = "view"
	// FileEditorCommandCreate 表示创建新文件。
	FileEditorCommandCreate FileEditorCommand = "create"
	// FileEditorCommandStrReplace 表示按字符串执行唯一替换。
	FileEditorCommandStrReplace FileEditorCommand = "str_replace"
	// FileEditorCommandInsert 表示按行插入文本。
	FileEditorCommandInsert FileEditorCommand = "insert"
	// FileEditorCommandUndoEdit 表示撤销最近一次编辑。
	FileEditorCommandUndoEdit FileEditorCommand = "undo_edit"
)

const (
	maxViewTextFileSizeBytes  = 5 * 1024 * 1024
	maxViewImageFileSizeBytes = 10 * 1024 * 1024
	maxViewOutputBytes        = 128 * 1024
	viewDirectoryMaxDepth     = 2
)

// FileEditorAction 表示文件编辑工具的输入参数。
type FileEditorAction struct {
	types.ToolMetadata `json:",inline"`
	Command            FileEditorCommand `json:"command" jsonschema:"description=文件编辑命令,enum=view,enum=create,enum=str_replace,enum=insert,enum=undo_edit"`
	Path               string            `json:"path" jsonschema:"description=目标文件路径,required"`
	FileText           *string           `json:"file_text,omitempty" jsonschema:"description=创建文件或写入时使用的完整文本"`
	OldStr             *string           `json:"old_str,omitempty" jsonschema:"description=待替换的旧字符串"`
	NewStr             *string           `json:"new_str,omitempty" jsonschema:"description=替换后的新字符串或插入文本"`
	InsertLine         *int              `json:"insert_line,omitempty" jsonschema:"description=插入发生的目标行号,从1开始"`
	ViewRange          []int             `json:"view_range,omitempty" jsonschema:"description=查看文件时的行号范围,格式为[start,end]"`
}

// ActionType 返回当前文件编辑动作对应的动作类型。
func (a *FileEditorAction) ActionType() types.ActionType {
	if a == nil {
		return types.ActionEdit
	}

	switch a.Command {
	case FileEditorCommandView:
		return types.ActionRead
	case FileEditorCommandCreate:
		return types.ActionWrite
	case FileEditorCommandStrReplace, FileEditorCommandInsert, FileEditorCommandUndoEdit:
		return types.ActionEdit
	default:
		return types.ActionEdit
	}
}

// FileEditorObservation 表示文件编辑工具的输出结果。
type FileEditorObservation struct {
	types.BaseObservation
	Command    *FileEditorCommand `json:"command,omitempty"`
	Path       *string            `json:"path,omitempty"`
	PrevExist  bool               `json:"prev_exist"`
	OldContent *string            `json:"old_content,omitempty"`
	NewContent *string            `json:"new_content,omitempty"`
}

// FileEditorToolError 表示文件编辑工具的基础错误类型。
type FileEditorToolError struct {
	Message string
	Cause   error
}

// Error 返回错误文本。
func (e *FileEditorToolError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Unwrap 返回底层错误，便于错误链分析。
func (e *FileEditorToolError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// EditorToolParameterMissingError 表示命令缺少必填参数。
type EditorToolParameterMissingError struct {
	FileEditorToolError
	Command   FileEditorCommand
	Parameter string
}

// EditorToolParameterInvalidError 表示命令参数不合法。
type EditorToolParameterInvalidError struct {
	FileEditorToolError
	Command   FileEditorCommand
	Parameter string
	Value     string
	Hint      string
}

// FileValidationError 表示文件路径或文件状态校验失败。
type FileValidationError struct {
	FileEditorToolError
	Path   string
	Reason string
}

// BuildErrorMessage 根据错误类型生成稳定的错误文本。
func BuildErrorMessage(err error) string {
	if err == nil {
		return "未知文件编辑错误"
	}

	var missingErr *EditorToolParameterMissingError
	if errors.As(err, &missingErr) {
		return fmt.Sprintf("参数缺失: command=%q, parameter=%q", missingErr.Command, missingErr.Parameter)
	}

	var invalidErr *EditorToolParameterInvalidError
	if errors.As(err, &invalidErr) {
		message := fmt.Sprintf("参数非法: command=%q, parameter=%q, value=%q", invalidErr.Command, invalidErr.Parameter, invalidErr.Value)
		if invalidErr.Hint != "" {
			message += ", hint=" + invalidErr.Hint
		}
		return message
	}

	var fileErr *FileValidationError
	if errors.As(err, &fileErr) {
		return fmt.Sprintf("文件校验失败: path=%q, reason=%s", fileErr.Path, fileErr.Reason)
	}

	return err.Error()
}

// NewEditorToolParameterMissingError 构造缺少参数错误。
func NewEditorToolParameterMissingError(command FileEditorCommand, parameter string) error {
	return &EditorToolParameterMissingError{
		FileEditorToolError: FileEditorToolError{
			Message: fmt.Sprintf("缺少必填参数 %q，命令为 %q", parameter, command),
		},
		Command:   command,
		Parameter: parameter,
	}
}

// NewEditorToolParameterInvalidError 构造非法参数错误。
func NewEditorToolParameterInvalidError(command FileEditorCommand, parameter, value, hint string) error {
	message := fmt.Sprintf("参数 %q 的值 %q 不合法，命令为 %q", parameter, value, command)
	if hint != "" {
		message += "：" + hint
	}
	return &EditorToolParameterInvalidError{
		FileEditorToolError: FileEditorToolError{
			Message: message,
		},
		Command:   command,
		Parameter: parameter,
		Value:     value,
		Hint:      hint,
	}
}

// NewFileValidationError 构造文件校验失败错误。
func NewFileValidationError(path, reason string, cause error) error {
	return &FileValidationError{
		FileEditorToolError: FileEditorToolError{
			Message: fmt.Sprintf("文件校验失败: path=%q, reason=%s", path, reason),
			Cause:   cause,
		},
		Path:   path,
		Reason: reason,
	}
}

// NewObservation 构造文件编辑工具的成功观察结果。
func NewObservation(command FileEditorCommand, path string, prevExist bool, oldContent, newContent *string, content ...types.Content) *FileEditorObservation {
	obs := &FileEditorObservation{
		BaseObservation: types.BaseObservation{
			BaseEvent: types.BaseEvent{
				Source: types.SourceEnvironment,
			},
			Content:     content,
			ErrorStatus: false,
		},
		Command:    &command,
		PrevExist:  prevExist,
		OldContent: oldContent,
		NewContent: newContent,
	}
	if trimmedPath := strings.TrimSpace(path); trimmedPath != "" {
		obs.Path = stringPtr(trimmedPath)
	}
	return obs
}

// NewErrorObservation 构造文件编辑工具的错误观察结果。
func NewErrorObservation(command FileEditorCommand, path string, err error) *FileEditorObservation {
	message := BuildErrorMessage(err)

	obs := &FileEditorObservation{
		BaseObservation: types.BaseObservation{
			BaseEvent: types.BaseEvent{
				Source: types.SourceEnvironment,
			},
			Content: []types.Content{
				types.TextContent{
					Text: message,
				},
			},
			ErrorStatus: true,
		},
		Command: &command,
	}
	if trimmedPath := strings.TrimSpace(path); trimmedPath != "" {
		obs.Path = stringPtr(trimmedPath)
	}
	return obs
}

func stringPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
