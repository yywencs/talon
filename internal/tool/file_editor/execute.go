package file_editor

import (
	"context"
	"fmt"
)

// Execute 是文件编辑工具的执行入口。
func Execute(ctx context.Context, action FileEditorAction) *FileEditorObservation {
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
		return NewErrorObservation(action.Command, action.Path, fmt.Errorf("unsupported file editor command: %s", action.Command))
	}
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
