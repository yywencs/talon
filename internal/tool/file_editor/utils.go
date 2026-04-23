package file_editor

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ValidateAction 校验文件编辑动作的基础合法性。
func ValidateAction(action FileEditorAction) error {
	if strings.TrimSpace(string(action.Command)) == "" {
		return NewEditorToolParameterMissingError(action.Command, "command")
	}
	if strings.TrimSpace(action.Path) == "" {
		return NewEditorToolParameterMissingError(action.Command, "path")
	}

	switch action.Command {
	case FileEditorCommandView:
		if len(action.ViewRange) != 0 && len(action.ViewRange) != 2 {
			return NewEditorToolParameterInvalidError(action.Command, "view_range", fmt.Sprintf("%v", action.ViewRange), "必须为空或包含 2 个整数")
		}
		if len(action.ViewRange) == 2 {
			if action.ViewRange[0] < 1 || action.ViewRange[1] < 1 {
				return NewEditorToolParameterInvalidError(action.Command, "view_range", fmt.Sprintf("%v", action.ViewRange), "行号必须从 1 开始")
			}
			if action.ViewRange[0] > action.ViewRange[1] {
				return NewEditorToolParameterInvalidError(action.Command, "view_range", fmt.Sprintf("%v", action.ViewRange), "起始行不能大于结束行")
			}
		}
	case FileEditorCommandCreate:
		if action.FileText == nil {
			return NewEditorToolParameterMissingError(action.Command, "file_text")
		}
	case FileEditorCommandStrReplace:
		if action.OldStr == nil {
			return NewEditorToolParameterMissingError(action.Command, "old_str")
		}
		if action.NewStr == nil {
			return NewEditorToolParameterMissingError(action.Command, "new_str")
		}
		if *action.OldStr == "" {
			return NewEditorToolParameterInvalidError(action.Command, "old_str", *action.OldStr, "old_str 不能为空字符串")
		}
	case FileEditorCommandInsert:
		if action.InsertLine == nil {
			return NewEditorToolParameterMissingError(action.Command, "insert_line")
		}
		if *action.InsertLine < 1 {
			return NewEditorToolParameterInvalidError(action.Command, "insert_line", strconv.Itoa(*action.InsertLine), "插入行号必须从 1 开始")
		}
		if action.NewStr == nil && action.FileText == nil {
			return NewEditorToolParameterMissingError(action.Command, "new_str_or_file_text")
		}
	case FileEditorCommandUndoEdit:
		return nil
	default:
		return NewEditorToolParameterInvalidError(action.Command, "command", string(action.Command), "不支持的文件编辑命令")
	}

	return nil
}

// ValidatePath 校验路径格式，并确保传入值是绝对路径。
func ValidatePath(path string) (string, error) {
	trimmedPath := strings.TrimSpace(path)
	if trimmedPath == "" {
		return "", NewEditorToolParameterMissingError("", "path")
	}

	if !filepath.IsAbs(trimmedPath) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", NewFileValidationError(trimmedPath, "获取当前工作目录失败", err)
		}
		suggestedPath := filepath.Clean(filepath.Join(cwd, trimmedPath))
		return "", NewEditorToolParameterInvalidError("", "path", trimmedPath, fmt.Sprintf("这个路径不是以/开头的绝对路径；你是指%q?", suggestedPath))
	}

	return filepath.Clean(trimmedPath), nil
}
