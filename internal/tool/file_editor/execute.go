package file_editor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/wen/opentalon/internal/types"
)

// Execute 执行文件编辑动作。
func (e *FileEditor) Execute(ctx context.Context, action FileEditorAction) *FileEditorObservation {
	if e == nil {
		return NewErrorObservation(action.Command, action.Path, fmt.Errorf("file editor is nil"))
	}
	if err := ValidateAction(action); err != nil {
		return NewErrorObservation(action.Command, action.Path, err)
	}

	validatedPath, err := ValidatePath(action.Path)
	if err != nil {
		return NewErrorObservation(action.Command, action.Path, err)
	}
	action.Path = validatedPath

	switch action.Command {
	case FileEditorCommandView:
		return e.executeView(ctx, action)
	case FileEditorCommandCreate:
		return e.executeCreate(ctx, action)
	case FileEditorCommandStrReplace:
		return e.executeStrReplace(ctx, action)
	case FileEditorCommandInsert:
		return e.executeInsert(ctx, action)
	case FileEditorCommandUndoEdit:
		return e.executeUndoEdit(ctx, action)
	default:
		return NewErrorObservation(action.Command, action.Path, fmt.Errorf("unsupported file editor command: %s", action.Command))
	}
}

// executeCreate 执行文件创建命令。
func (e *FileEditor) executeCreate(ctx context.Context, action FileEditorAction) *FileEditorObservation {
	_ = ctx

	if action.FileText == nil {
		return NewErrorObservation(action.Command, action.Path, NewEditorToolParameterMissingError(action.Command, "file_text"))
	}
	if int64(len(*action.FileText)) > e.maxFileSize {
		return NewErrorObservation(
			action.Command,
			action.Path,
			NewFileValidationError(action.Path, fmt.Sprintf("文件内容太大：%.2fMB，最大允许 %dMB", bytesToMB(int64(len(*action.FileText))), e.MAX_FILE_SIZE_MB), nil),
		)
	}
	if info, err := os.Stat(action.Path); err == nil {
		if info.IsDir() {
			return NewErrorObservation(action.Command, action.Path, NewFileValidationError(action.Path, "目标路径已存在且是目录", nil))
		}
		return NewErrorObservation(action.Command, action.Path, NewFileValidationError(action.Path, "目标文件已存在", nil))
	} else if !os.IsNotExist(err) {
		return NewErrorObservation(action.Command, action.Path, NewFileValidationError(action.Path, "检查目标路径状态失败", err))
	}

	parentDir := filepath.Dir(action.Path)
	parentInfo, err := os.Stat(parentDir)
	if err != nil {
		if os.IsNotExist(err) {
			return NewErrorObservation(action.Command, action.Path, NewFileValidationError(action.Path, "父目录不存在", err))
		}
		return NewErrorObservation(action.Command, action.Path, NewFileValidationError(action.Path, "检查父目录状态失败", err))
	}
	if !parentInfo.IsDir() {
		return NewErrorObservation(action.Command, action.Path, NewFileValidationError(action.Path, "父路径不是目录", nil))
	}

	file, err := os.OpenFile(action.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return NewErrorObservation(action.Command, action.Path, NewFileValidationError(action.Path, "创建文件失败", err))
	}
	if _, writeErr := file.WriteString(*action.FileText); writeErr != nil {
		_ = file.Close()
		_ = os.Remove(action.Path)
		return NewErrorObservation(action.Command, action.Path, NewFileValidationError(action.Path, "写入文件失败", writeErr))
	}
	if closeErr := file.Close(); closeErr != nil {
		_ = os.Remove(action.Path)
		return NewErrorObservation(action.Command, action.Path, NewFileValidationError(action.Path, "关闭文件失败", closeErr))
	}

	if err := e.appendVersionToHistoryChain(action.Path, ""); err != nil {
		_ = os.Remove(action.Path)
		return NewErrorObservation(action.Command, action.Path, fmt.Errorf("append create operation to version chain: %w", err))
	}

	return NewObservation(
		action.Command,
		action.Path,
		false,
		nil,
		action.FileText,
		types.TextContent{Text: fmt.Sprintf("文件 %q 创建成功。", action.Path)},
	)
}

// executeStrReplace 执行字符串替换命令。
func (e *FileEditor) executeStrReplace(ctx context.Context, action FileEditorAction) *FileEditorObservation {
	_ = ctx

	info, err := os.Stat(action.Path)
	if err != nil {
		return NewErrorObservation(action.Command, action.Path, NewFileValidationError(action.Path, "目标文件不存在或不可访问", err))
	}
	if info.IsDir() {
		return NewErrorObservation(action.Command, action.Path, NewFileValidationError(action.Path, "目标路径是目录", nil))
	}
	if info.Size() > e.maxFileSize {
		return NewErrorObservation(
			action.Command,
			action.Path,
			NewFileValidationError(action.Path, fmt.Sprintf("文件太大：%.2fMB，最大允许 %dMB", bytesToMB(info.Size()), e.MAX_FILE_SIZE_MB), nil),
		)
	}

	oldContent, fileEncoding, err := readTextFile(action.Path)
	if err != nil {
		return NewErrorObservation(action.Command, action.Path, NewFileValidationError(action.Path, fmt.Sprintf("读取文本文件失败: %v", err), err))
	}
	if !fileEncoding.Editable {
		reason := fmt.Sprintf("无法确认文件编码，当前仅允许查看，不允许编辑；检测结果为 %s", fileEncoding.Name)
		if fileEncoding.Reason != "" {
			reason += "，" + fileEncoding.Reason
		}
		return NewErrorObservation(action.Command, action.Path, NewFileValidationError(action.Path, reason, nil))
	}
	pattern := *action.OldStr
	replacement := *action.NewStr
	matchRegexp, err := regexp.Compile(pattern)
	if err != nil {
		return NewErrorObservation(action.Command, action.Path, NewEditorToolParameterInvalidError(action.Command, "old_str", pattern, fmt.Sprintf("不是合法正则表达式: %v", err)))
	}

	matches := matchRegexp.FindAllStringIndex(oldContent, -1)
	usedTrimmedRetry := false
	if len(matches) == 0 {
		trimmedPattern := strings.TrimSpace(pattern)
		trimmedReplacement := strings.TrimSpace(replacement)
		if trimmedPattern != "" && (trimmedPattern != pattern || trimmedReplacement != replacement) {
			matchRegexp, err = regexp.Compile(trimmedPattern)
			if err != nil {
				return NewErrorObservation(action.Command, action.Path, NewEditorToolParameterInvalidError(action.Command, "old_str", trimmedPattern, fmt.Sprintf("去除首尾空白后不是合法正则表达式: %v", err)))
			}
			matches = matchRegexp.FindAllStringIndex(oldContent, -1)
			pattern = trimmedPattern
			replacement = trimmedReplacement
			usedTrimmedRetry = true
		}
	}

	if len(matches) == 0 {
		return NewErrorObservation(action.Command, action.Path, fmt.Errorf("没有进行替换，因为没有找到旧字符串"))
	}
	if len(matches) > 1 {
		return NewErrorObservation(action.Command, action.Path, fmt.Errorf("没有进行替换，因为找到多处旧字符串"))
	}
	if err := e.appendVersionToHistoryChain(action.Path, oldContent); err != nil {
		return NewErrorObservation(action.Command, action.Path, fmt.Errorf("append replace operation to version chain: %w", err))
	}

	newContent := matchRegexp.ReplaceAllString(oldContent, replacement)
	if err := writeTextFile(action.Path, newContent, fileEncoding, 0o644); err != nil {
		if rollbackErr := e.rollbackLatestVersionFromHistoryChain(action.Path); rollbackErr != nil {
			return NewErrorObservation(action.Command, action.Path, fmt.Errorf("写回文件失败，且回滚版本链失败: write_err=%v rollback_err=%v", err, rollbackErr))
		}
		return NewErrorObservation(action.Command, action.Path, NewFileValidationError(action.Path, "写回文件失败", err))
	}

	changedLinesPreview := buildChangedLinesPreview(oldContent, newContent, matches[0][0])
	message := fmt.Sprintf("已完成替换。受影响的行如下：\n%s\n\n检查一下看是否符合预期，否则可以重新编辑。", changedLinesPreview)
	if usedTrimmedRetry {
		message = fmt.Sprintf("已完成替换。本次匹配使用了去除首尾空白后的 old_str 和 new_str。受影响的行如下：\n%s\n\n检查一下看是否符合预期，否则可以重新编辑。", changedLinesPreview)
	}

	return NewObservation(
		action.Command,
		action.Path,
		true,
		&oldContent,
		&newContent,
		types.TextContent{Text: message},
	)
}

func buildChangedLinesPreview(oldContent, newContent string, matchStart int) string {
	oldLines := splitTextLines(oldContent)
	newLines := splitTextLines(newContent)

	prefixLen := 0
	maxPrefix := minInt(len(oldLines), len(newLines))
	for prefixLen < maxPrefix && oldLines[prefixLen] == newLines[prefixLen] {
		prefixLen++
	}

	oldSuffix := len(oldLines) - 1
	newSuffix := len(newLines) - 1
	for oldSuffix >= prefixLen && newSuffix >= prefixLen && oldLines[oldSuffix] == newLines[newSuffix] {
		oldSuffix--
		newSuffix--
	}

	if len(newLines) == 0 {
		return "(变更后文件为空)"
	}
	if prefixLen <= newSuffix {
		return formatLinesWithNumbers(newLines[prefixLen:newSuffix+1], prefixLen+1)
	}

	lineNumber := 1
	if matchStart > 0 {
		lineNumber += strings.Count(oldContent[:matchStart], "\n")
	}
	if lineNumber < 1 {
		lineNumber = 1
	}
	if lineNumber > len(newLines) {
		lineNumber = len(newLines)
	}
	if lineNumber <= 0 {
		return "(变更后该位置无可展示行)"
	}
	return formatLinesWithNumbers(newLines[lineNumber-1:lineNumber], lineNumber)
}

// executeInsert 执行按行插入命令。
func (e *FileEditor) executeInsert(ctx context.Context, action FileEditorAction) *FileEditorObservation {
	_ = ctx
	_ = e
	_ = action
	panic("not implemented")
}

// executeUndoEdit 执行撤销编辑命令。
func (e *FileEditor) executeUndoEdit(ctx context.Context, action FileEditorAction) *FileEditorObservation {
	_ = ctx
	_ = e
	_ = action
	panic("not implemented")
}
