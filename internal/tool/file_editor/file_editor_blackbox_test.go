package file_editor_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fileeditor "github.com/wen/opentalon/internal/tool/file_editor"
	"github.com/wen/opentalon/internal/types"
)

func textPtr(v string) *string {
	return &v
}

func intPtr(v int) *int {
	return &v
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}
}

func observationText(obs *fileeditor.FileEditorObservation) string {
	tokens := make([]string, 0)
	if obs == nil {
		return ""
	}
	for _, content := range obs.GetContent() {
		if text, ok := content.(types.TextContent); ok {
			tokens = append(tokens, text.Text)
		}
	}
	return strings.Join(tokens, "\n")
}

func newEditor(t *testing.T) *fileeditor.FileEditor {
	t.Helper()
	editor, err := fileeditor.NewDefaultFileEditor()
	if err != nil {
		t.Fatalf("NewDefaultFileEditor() error = %v", err)
	}
	return editor
}

func TestFileEditorBlackbox_NilReceiverReturnsErrorObservation(t *testing.T) {
	var editor *fileeditor.FileEditor

	obs := editor.Execute(context.Background(), fileeditor.FileEditorAction{
		Command: fileeditor.FileEditorCommandView,
		Path:    "/tmp/blackbox-nil.txt",
	})
	if obs == nil || !obs.IsError() {
		t.Fatal("expected nil receiver to return error observation")
	}
	if !strings.Contains(observationText(obs), "file editor is nil") {
		t.Fatalf("error text = %q, want nil receiver message", observationText(obs))
	}
}

func TestFileEditorBlackbox_ParameterValidation(t *testing.T) {
	editor := newEditor(t)
	absPath := filepath.Join(t.TempDir(), "target.txt")

	testCases := []struct {
		name        string
		action      fileeditor.FileEditorAction
		wantSubText string
	}{
		{
			name: "create missing file_text",
			action: fileeditor.FileEditorAction{
				Command: fileeditor.FileEditorCommandCreate,
				Path:    absPath,
			},
			wantSubText: "file_text",
		},
		{
			name: "str_replace missing old_str",
			action: fileeditor.FileEditorAction{
				Command: fileeditor.FileEditorCommandStrReplace,
				Path:    absPath,
				NewStr:  textPtr("new"),
			},
			wantSubText: "old_str",
		},
		{
			name: "str_replace missing new_str",
			action: fileeditor.FileEditorAction{
				Command: fileeditor.FileEditorCommandStrReplace,
				Path:    absPath,
				OldStr:  textPtr("old"),
			},
			wantSubText: "new_str",
		},
		{
			name: "insert missing insert_line",
			action: fileeditor.FileEditorAction{
				Command: fileeditor.FileEditorCommandInsert,
				Path:    absPath,
				NewStr:  textPtr("content"),
			},
			wantSubText: "insert_line",
		},
		{
			name: "insert missing text payload",
			action: fileeditor.FileEditorAction{
				Command:    fileeditor.FileEditorCommandInsert,
				Path:       absPath,
				InsertLine: intPtr(1),
			},
			wantSubText: "new_str_or_file_text",
		},
		{
			name: "relative path rejected",
			action: fileeditor.FileEditorAction{
				Command: fileeditor.FileEditorCommandView,
				Path:    "relative/path.txt",
			},
			wantSubText: "不是以/开头的绝对路径",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			obs := editor.Execute(context.Background(), tc.action)
			if obs == nil || !obs.IsError() {
				t.Fatal("expected validation failure to return error observation")
			}
			if !strings.Contains(observationText(obs), tc.wantSubText) {
				t.Fatalf("error text = %q, want substring %q", observationText(obs), tc.wantSubText)
			}
		})
	}
}

func TestFileEditorBlackbox_CreateReplaceUndoLifecycle(t *testing.T) {
	editor := newEditor(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "lifecycle.txt")

	createObs := editor.Execute(context.Background(), fileeditor.FileEditorAction{
		Command:  fileeditor.FileEditorCommandCreate,
		Path:     path,
		FileText: textPtr("hello\n"),
	})
	if createObs == nil || createObs.IsError() {
		t.Fatalf("expected create success, got %q", observationText(createObs))
	}
	if createObs.PrevExist {
		t.Fatal("PrevExist = true, want false")
	}
	if !strings.Contains(observationText(createObs), path) {
		t.Fatalf("create text = %q, want file path", observationText(createObs))
	}

	replaceObs := editor.Execute(context.Background(), fileeditor.FileEditorAction{
		Command: fileeditor.FileEditorCommandStrReplace,
		Path:    path,
		OldStr:  textPtr("hello"),
		NewStr:  textPtr("world"),
	})
	if replaceObs == nil || replaceObs.IsError() {
		t.Fatalf("expected replace success, got %q", observationText(replaceObs))
	}

	undoObs := editor.Execute(context.Background(), fileeditor.FileEditorAction{
		Command: fileeditor.FileEditorCommandUndoEdit,
		Path:    path,
	})
	if undoObs == nil || undoObs.IsError() {
		t.Fatalf("expected undo success, got %q", observationText(undoObs))
	}
	if undoObs.OldContent == nil || *undoObs.OldContent != "world\n" {
		t.Fatalf("OldContent = %v, want %q", undoObs.OldContent, "world\n")
	}
	if undoObs.NewContent == nil || *undoObs.NewContent != "hello\n" {
		t.Fatalf("NewContent = %v, want %q", undoObs.NewContent, "hello\n")
	}
	if !strings.Contains(observationText(undoObs), "已撤销最近一次编辑") {
		t.Fatalf("undo text = %q, want undo summary", observationText(undoObs))
	}
	if !strings.Contains(observationText(undoObs), path) {
		t.Fatalf("undo text = %q, want file path", observationText(undoObs))
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "hello\n" {
		t.Fatalf("file content = %q, want %q", string(data), "hello\n")
	}
}

func TestFileEditorBlackbox_InsertBoundariesAndPreview(t *testing.T) {
	editor := newEditor(t)

	t.Run("out of range line fails", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "range.txt")
		writeFile(t, path, "a\nb\n")

		obs := editor.Execute(context.Background(), fileeditor.FileEditorAction{
			Command:    fileeditor.FileEditorCommandInsert,
			Path:       path,
			InsertLine: intPtr(100),
			NewStr:     textPtr("x"),
		})
		if obs == nil || !obs.IsError() {
			t.Fatal("expected insert out of range to fail")
		}
		if !strings.Contains(observationText(obs), "超出允许范围") {
			t.Fatalf("error text = %q, want out-of-range hint", observationText(obs))
		}
	})

	t.Run("empty file insert succeeds", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "empty.txt")
		writeFile(t, path, "")

		obs := editor.Execute(context.Background(), fileeditor.FileEditorAction{
			Command:    fileeditor.FileEditorCommandInsert,
			Path:       path,
			InsertLine: intPtr(1),
			NewStr:     textPtr("hello"),
		})
		if obs == nil || obs.IsError() {
			t.Fatalf("expected insert success, got %q", observationText(obs))
		}
		if !strings.Contains(observationText(obs), path) {
			t.Fatalf("insert text = %q, want file path", observationText(obs))
		}
		if !strings.Contains(observationText(obs), "1| hello") {
			t.Fatalf("insert text = %q, want line preview", observationText(obs))
		}
	})
}

func TestFileEditorBlackbox_ViewMarkdownDoesNotAddLineNumbers(t *testing.T) {
	editor := newEditor(t)
	path := filepath.Join(t.TempDir(), "README.md")
	writeFile(t, path, "# Title\n\nbody\n")

	obs := editor.Execute(context.Background(), fileeditor.FileEditorAction{
		Command: fileeditor.FileEditorCommandView,
		Path:    path,
	})
	if obs == nil || obs.IsError() {
		t.Fatalf("expected markdown view success, got %q", observationText(obs))
	}
	text := observationText(obs)
	if !strings.Contains(text, "# Title") {
		t.Fatalf("view text = %q, want markdown content", text)
	}
	if strings.Contains(text, "1| # Title") {
		t.Fatalf("view text = %q, markdown preview should not add line numbers", text)
	}
}

func TestFileEditorBlackbox_UndoWithoutHistoryReturnsError(t *testing.T) {
	editor := newEditor(t)
	path := filepath.Join(t.TempDir(), "plain.txt")
	writeFile(t, path, "plain\n")

	obs := editor.Execute(context.Background(), fileeditor.FileEditorAction{
		Command: fileeditor.FileEditorCommandUndoEdit,
		Path:    path,
	})
	if obs == nil || !obs.IsError() {
		t.Fatal("expected undo without history to fail")
	}
	if !strings.Contains(observationText(obs), "没有编辑历史") {
		t.Fatalf("error text = %q, want no-history message", observationText(obs))
	}
}
