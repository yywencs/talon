package tool

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

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

var supportedImageMIMEs = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
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

// fileEditorExecutor 是文件编辑工具的执行入口。
func fileEditorExecutor(ctx context.Context, action FileEditorAction) *FileEditorObservation {
	if err := validateFileEditorAction(action); err != nil {
		return newFileEditorErrorObservation(action.Command, action.Path, err)
	}

	validatedPath, err := validatePath(action.Path)
	if err != nil {
		return newFileEditorErrorObservation(action.Command, action.Path, err)
	}
	action.Path = validatedPath

	switch action.Command {
	case FileEditorCommandView:
		return executeView(ctx, action)
	case FileEditorCommandCreate:
		return executeCreate(ctx, action)
	case FileEditorCommandStrReplace:
		return executeStrReplace(ctx, action)
	case FileEditorCommandInsert:
		return executeInsert(ctx, action)
	case FileEditorCommandUndoEdit:
		return executeUndoEdit(ctx, action)
	default:
		return newFileEditorErrorObservation(action.Command, action.Path, fmt.Errorf("unsupported file editor command: %s", action.Command))
	}
}

// validateFileEditorAction 校验文件编辑动作的基础合法性。
func validateFileEditorAction(action FileEditorAction) error {
	if strings.TrimSpace(string(action.Command)) == "" {
		return newEditorToolParameterMissingError(action.Command, "command")
	}
	if strings.TrimSpace(action.Path) == "" {
		return newEditorToolParameterMissingError(action.Command, "path")
	}

	switch action.Command {
	case FileEditorCommandView:
		if len(action.ViewRange) != 0 && len(action.ViewRange) != 2 {
			return newEditorToolParameterInvalidError(action.Command, "view_range", fmt.Sprintf("%v", action.ViewRange), "必须为空或包含 2 个整数")
		}
		if len(action.ViewRange) == 2 {
			if action.ViewRange[0] < 1 || action.ViewRange[1] < 1 {
				return newEditorToolParameterInvalidError(action.Command, "view_range", fmt.Sprintf("%v", action.ViewRange), "行号必须从 1 开始")
			}
			if action.ViewRange[0] > action.ViewRange[1] {
				return newEditorToolParameterInvalidError(action.Command, "view_range", fmt.Sprintf("%v", action.ViewRange), "起始行不能大于结束行")
			}
		}
	case FileEditorCommandCreate:
		if action.FileText == nil {
			return newEditorToolParameterMissingError(action.Command, "file_text")
		}
	case FileEditorCommandStrReplace:
		if action.OldStr == nil {
			return newEditorToolParameterMissingError(action.Command, "old_str")
		}
		if action.NewStr == nil {
			return newEditorToolParameterMissingError(action.Command, "new_str")
		}
		if *action.OldStr == "" {
			return newEditorToolParameterInvalidError(action.Command, "old_str", *action.OldStr, "old_str 不能为空字符串")
		}
	case FileEditorCommandInsert:
		if action.InsertLine == nil {
			return newEditorToolParameterMissingError(action.Command, "insert_line")
		}
		if *action.InsertLine < 1 {
			return newEditorToolParameterInvalidError(action.Command, "insert_line", strconv.Itoa(*action.InsertLine), "插入行号必须从 1 开始")
		}
		if action.NewStr == nil && action.FileText == nil {
			return newEditorToolParameterMissingError(action.Command, "new_str_or_file_text")
		}
	case FileEditorCommandUndoEdit:
		return nil
	default:
		return newEditorToolParameterInvalidError(action.Command, "command", string(action.Command), "不支持的文件编辑命令")
	}

	return nil
}

// validatePath 校验路径格式，并确保传入值是绝对路径。
func validatePath(path string) (string, error) {
	trimmedPath := strings.TrimSpace(path)
	if trimmedPath == "" {
		return "", newEditorToolParameterMissingError("", "path")
	}

	if !filepath.IsAbs(trimmedPath) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", newFileValidationError(trimmedPath, "获取当前工作目录失败", err)
		}
		suggestedPath := filepath.Clean(filepath.Join(cwd, trimmedPath))
		return "", newEditorToolParameterInvalidError("", "path", trimmedPath, fmt.Sprintf("这个路径不是以/开头的绝对路径；你是指%q?", suggestedPath))
	}

	cleanedPath := filepath.Clean(trimmedPath)
	return cleanedPath, nil
}

// executeView 执行文件查看命令。
func executeView(ctx context.Context, action FileEditorAction) *FileEditorObservation {
	_ = ctx

	info, err := os.Stat(action.Path)
	if err != nil {
		return newFileEditorErrorObservation(action.Command, action.Path, newFileValidationError(action.Path, "路径不存在或不可访问", err))
	}

	if info.IsDir() {
		content, hiddenCount, buildErr := buildDirectoryViewContent(action.Path)
		if buildErr != nil {
			return newFileEditorErrorObservation(action.Command, action.Path, buildErr)
		}
		if hiddenCount > 0 {
			content += fmt.Sprintf("\n\n已排除 %d 个隐藏文件或文件夹，可通过 ls -la 查看。", hiddenCount)
		}
		return newFileEditorObservation(action.Command, action.Path, true, nil, nil, types.TextContent{Text: content})
	}

	if imageMIME, ok := detectSupportedImageMIME(action.Path); ok {
		return executeViewImage(action, info, imageMIME)
	}

	if info.Size() > maxViewTextFileSizeBytes {
		return newFileEditorErrorObservation(action.Command, action.Path, newFileValidationError(action.Path, fmt.Sprintf("文件太大：%.2fMB，最大允许 %.2fMB", bytesToMB(info.Size()), bytesToMB(maxViewTextFileSizeBytes)), nil))
	}

	data, err := os.ReadFile(action.Path)
	if err != nil {
		return newFileEditorErrorObservation(action.Command, action.Path, newFileValidationError(action.Path, "读取文件失败", err))
	}
	if isBinaryContent(data) {
		return newFileEditorErrorObservation(action.Command, action.Path, newFileValidationError(action.Path, "检测到二进制文件，且不是受支持的图像文件", nil))
	}

	textContent, err := buildTextFileViewContent(action.Path, string(data), action.ViewRange)
	if err != nil {
		return newFileEditorErrorObservation(action.Command, action.Path, err)
	}
	return newFileEditorObservation(action.Command, action.Path, true, nil, nil, types.TextContent{Text: textContent})
}

func executeViewImage(action FileEditorAction, info os.FileInfo, imageMIME string) *FileEditorObservation {
	if info.Size() > maxViewImageFileSizeBytes {
		return newFileEditorErrorObservation(action.Command, action.Path, newFileValidationError(action.Path, fmt.Sprintf("图片太大：%.2fMB，最大允许 %.2fMB", bytesToMB(info.Size()), bytesToMB(maxViewImageFileSizeBytes)), nil))
	}

	data, err := os.ReadFile(action.Path)
	if err != nil {
		return newFileEditorErrorObservation(action.Command, action.Path, newFileValidationError(action.Path, "读取图片文件失败", err))
	}
	if !isDetectedImage(data) {
		return newFileEditorErrorObservation(action.Command, action.Path, newFileValidationError(action.Path, "文件后缀是图片，但文件内容不是有效图像", nil))
	}

	imageURI := "data:" + imageMIME + ";base64," + base64.StdEncoding.EncodeToString(data)
	text := fmt.Sprintf("图片文件 %q 读取成功，图像内容如下。", action.Path)
	return newFileEditorObservation(
		action.Command,
		action.Path,
		true,
		nil,
		nil,
		types.TextContent{Text: text},
		types.ImageContent{ImageURLs: []string{imageURI}},
	)
}

// executeCreate 执行文件创建命令。
func executeCreate(ctx context.Context, action FileEditorAction) *FileEditorObservation {
	_ = ctx
	_ = action
	panic("not implemented")
}

// executeStrReplace 执行字符串替换命令。
func executeStrReplace(ctx context.Context, action FileEditorAction) *FileEditorObservation {
	_ = ctx
	_ = action
	panic("not implemented")
}

// executeInsert 执行按行插入命令。
func executeInsert(ctx context.Context, action FileEditorAction) *FileEditorObservation {
	_ = ctx
	_ = action
	panic("not implemented")
}

// executeUndoEdit 执行撤销编辑命令。
func executeUndoEdit(ctx context.Context, action FileEditorAction) *FileEditorObservation {
	_ = ctx
	_ = action
	panic("not implemented")
}

func buildDirectoryViewContent(path string) (string, int, error) {
	lines := []string{
		fmt.Sprintf("这是两层深度的文件和文件夹列表：%s", path),
	}

	hiddenCount := 0
	if err := appendDirectoryEntries(path, 0, &lines, &hiddenCount); err != nil {
		return "", 0, newFileValidationError(path, "读取目录内容失败", err)
	}
	return strings.Join(lines, "\n"), hiddenCount, nil
}

func appendDirectoryEntries(path string, depth int, lines *[]string, hiddenCount *int) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	indent := strings.Repeat("  ", depth)
	for _, entry := range entries {
		name := entry.Name()
		if isHiddenName(name) {
			*hiddenCount++
			continue
		}

		displayName := indent + name
		if entry.IsDir() {
			displayName += "{}"
		}
		*lines = append(*lines, displayName)

		if entry.IsDir() && depth+1 < viewDirectoryMaxDepth {
			if err := appendDirectoryEntries(filepath.Join(path, name), depth+1, lines, hiddenCount); err != nil {
				return err
			}
		}
	}
	return nil
}

func detectSupportedImageMIME(path string) (string, bool) {
	imageMIME, ok := supportedImageMIMEs[strings.ToLower(filepath.Ext(path))]
	return imageMIME, ok
}

func isDetectedImage(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	return strings.HasPrefix(http.DetectContentType(data[:minInt(len(data), 512)]), "image/")
}

func buildTextFileViewContent(path, content string, viewRange []int) (string, error) {
	lines := splitTextLines(content)
	selectedStart, selectedEnd, err := resolveViewRange(lines, viewRange)
	if err != nil {
		return "", newFileValidationError(path, err.Error(), nil)
	}

	var builder strings.Builder
	if len(viewRange) == 2 {
		_, _ = fmt.Fprintf(&builder, "文件 %q 第 %d-%d 行内容如下：\n", path, selectedStart, selectedEnd)
	} else {
		_, _ = fmt.Fprintf(&builder, "文件 %q 全部内容如下：\n", path)
	}

	if len(lines) == 0 {
		builder.WriteString("(空文件)")
		return builder.String(), nil
	}

	numberedContent := formatLinesWithNumbers(lines[selectedStart-1:selectedEnd], selectedStart)
	builder.WriteString(numberedContent)
	return truncateViewContent(builder.String()), nil
}

func splitTextLines(content string) []string {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	if normalized == "" {
		return nil
	}

	lines := strings.Split(normalized, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func resolveViewRange(lines []string, viewRange []int) (int, int, error) {
	if len(viewRange) == 0 {
		if len(lines) == 0 {
			return 0, 0, nil
		}
		return 1, len(lines), nil
	}

	start := viewRange[0]
	end := viewRange[1]
	if start < 1 || end < 1 {
		return 0, 0, fmt.Errorf("view_range 行号必须从 1 开始")
	}
	if start > end {
		return 0, 0, fmt.Errorf("view_range 起始行不能大于结束行")
	}
	if len(lines) == 0 {
		return 0, 0, fmt.Errorf("空文件没有可查看的行内容")
	}
	if start > len(lines) {
		return 0, 0, fmt.Errorf("view_range 起始行超出文件总行数 %d", len(lines))
	}
	if end > len(lines) {
		end = len(lines)
	}
	return start, end, nil
}

func formatLinesWithNumbers(lines []string, startLine int) string {
	endLine := startLine + len(lines) - 1
	width := len(strconv.Itoa(endLine))
	formatted := make([]string, 0, len(lines))
	for idx, line := range lines {
		formatted = append(formatted, fmt.Sprintf("%*d| %s", width, startLine+idx, line))
	}
	return strings.Join(formatted, "\n")
}

func truncateViewContent(content string) string {
	if len(content) <= maxViewOutputBytes {
		return content
	}
	return content[:maxViewOutputBytes] + "\n...[truncated]"
}

func isBinaryContent(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	sample := data[:minInt(len(data), 8192)]
	if strings.IndexByte(string(sample), 0) >= 0 {
		return true
	}
	return !utf8.Valid(sample)
}

func isHiddenName(name string) bool {
	return strings.HasPrefix(name, ".")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func bytesToMB(size int64) float64 {
	return float64(size) / 1024.0 / 1024.0
}

func newFileEditorObservation(command FileEditorCommand, path string, prevExist bool, oldContent, newContent *string, content ...types.Content) *FileEditorObservation {
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

// newFileEditorErrorObservation 构造文件编辑工具的错误观察结果。
func newFileEditorErrorObservation(command FileEditorCommand, path string, err error) *FileEditorObservation {
	message := buildFileEditorErrorMessage(err)

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

// buildFileEditorErrorMessage 根据错误类型生成稳定的错误文本。
func buildFileEditorErrorMessage(err error) string {
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

// newEditorToolParameterMissingError 构造缺少参数错误。
func newEditorToolParameterMissingError(command FileEditorCommand, parameter string) error {
	return &EditorToolParameterMissingError{
		FileEditorToolError: FileEditorToolError{
			Message: fmt.Sprintf("缺少必填参数 %q，命令为 %q", parameter, command),
		},
		Command:   command,
		Parameter: parameter,
	}
}

// newEditorToolParameterInvalidError 构造非法参数错误。
func newEditorToolParameterInvalidError(command FileEditorCommand, parameter, value, hint string) error {
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

// newFileValidationError 构造文件校验失败错误。
func newFileValidationError(path, reason string, cause error) error {
	return &FileValidationError{
		FileEditorToolError: FileEditorToolError{
			Message: fmt.Sprintf("文件校验失败: path=%q, reason=%s", path, reason),
			Cause:   cause,
		},
		Path:   path,
		Reason: reason,
	}
}
