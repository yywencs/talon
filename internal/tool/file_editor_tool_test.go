package tool

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wen/opentalon/internal/types"
)

func strptr(v string) *string {
	return &v
}

func intptr(v int) *int {
	return &v
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}
}

func observationText(obs *FileEditorObservation) string {
	if obs == nil {
		return ""
	}

	var texts []string
	for _, content := range obs.GetContent() {
		if text, ok := content.(types.TextContent); ok {
			texts = append(texts, text.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func observationImageURLs(obs *FileEditorObservation) []string {
	if obs == nil {
		return nil
	}

	var urls []string
	for _, content := range obs.GetContent() {
		if image, ok := content.(types.ImageContent); ok {
			urls = append(urls, image.ImageURLs...)
		}
	}
	return urls
}

func runFileEditorExecutor(t *testing.T, action FileEditorAction) *FileEditorObservation {
	t.Helper()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("fileEditorExecutor 不应 panic: %v", r)
		}
	}()

	return fileEditorExecutor(context.Background(), action)
}

func TestFileEditorActionType(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		action   *FileEditorAction
		expected types.ActionType
	}{
		{
			name:     "nil action defaults to edit",
			action:   nil,
			expected: types.ActionEdit,
		},
		{
			name: "view maps to read",
			action: &FileEditorAction{
				Command: FileEditorCommandView,
			},
			expected: types.ActionRead,
		},
		{
			name: "create maps to write",
			action: &FileEditorAction{
				Command: FileEditorCommandCreate,
			},
			expected: types.ActionWrite,
		},
		{
			name: "replace maps to edit",
			action: &FileEditorAction{
				Command: FileEditorCommandStrReplace,
			},
			expected: types.ActionEdit,
		},
		{
			name: "unknown defaults to edit",
			action: &FileEditorAction{
				Command: FileEditorCommand("unknown"),
			},
			expected: types.ActionEdit,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.action.ActionType(); got != tc.expected {
				t.Fatalf("ActionType() = %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestFileEditorErrors_AsAndFormatting(t *testing.T) {
	t.Parallel()

	cause := errors.New("disk failure")
	err := newFileValidationError("/tmp/demo.txt", "读取失败", cause)

	var fileErr *FileValidationError
	if !errors.As(err, &fileErr) {
		t.Fatal("expected FileValidationError")
	}
	if fileErr.Path != "/tmp/demo.txt" {
		t.Fatalf("Path = %q, want /tmp/demo.txt", fileErr.Path)
	}
	if !errors.Is(err, cause) {
		t.Fatal("expected wrapped cause to be discoverable with errors.Is")
	}

	got := buildFileEditorErrorMessage(err)
	if !strings.Contains(got, "文件校验失败") {
		t.Fatalf("buildFileEditorErrorMessage() = %q, want contains 文件校验失败", got)
	}
}

func TestValidateFileEditorAction_CurrentValidation(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		action    FileEditorAction
		targetErr any
	}{
		{
			name: "missing command",
			action: FileEditorAction{
				Path: "/tmp/demo.txt",
			},
			targetErr: &EditorToolParameterMissingError{},
		},
		{
			name: "missing path",
			action: FileEditorAction{
				Command: FileEditorCommandView,
			},
			targetErr: &EditorToolParameterMissingError{},
		},
		{
			name: "view invalid view range length",
			action: FileEditorAction{
				Command:   FileEditorCommandView,
				Path:      "/tmp/demo.txt",
				ViewRange: []int{1},
			},
			targetErr: &EditorToolParameterInvalidError{},
		},
		{
			name: "create missing file_text",
			action: FileEditorAction{
				Command: FileEditorCommandCreate,
				Path:    "/tmp/demo.txt",
			},
			targetErr: &EditorToolParameterMissingError{},
		},
		{
			name: "replace missing old_str",
			action: FileEditorAction{
				Command: FileEditorCommandStrReplace,
				Path:    "/tmp/demo.txt",
				NewStr:  strptr("new"),
			},
			targetErr: &EditorToolParameterMissingError{},
		},
		{
			name: "insert missing insert_line",
			action: FileEditorAction{
				Command: FileEditorCommandInsert,
				Path:    "/tmp/demo.txt",
				NewStr:  strptr("hello"),
			},
			targetErr: &EditorToolParameterMissingError{},
		},
		{
			name: "unsupported command",
			action: FileEditorAction{
				Command: FileEditorCommand("boom"),
				Path:    "/tmp/demo.txt",
			},
			targetErr: &EditorToolParameterInvalidError{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateFileEditorAction(tc.action)
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if !errors.As(err, &tc.targetErr) {
				t.Fatalf("validateFileEditorAction() error = %T, want assignable to %T", err, tc.targetErr)
			}
		})
	}
}

func TestValidatePath_EmptyPath(t *testing.T) {
	t.Parallel()

	_, err := validatePath("   ")
	if err == nil {
		t.Fatal("expected error for empty path")
	}

	var missingErr *EditorToolParameterMissingError
	if !errors.As(err, &missingErr) {
		t.Fatalf("validatePath() error = %T, want *EditorToolParameterMissingError", err)
	}
}

func TestValidatePath_ShouldSuggestAbsolutePathForRelativeInput(t *testing.T) {
	t.Parallel()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("获取当前工作目录失败: %v", err)
	}

	input := "../../etc/passwd"
	_, err = validatePath(input)
	if err == nil {
		t.Fatal("expected relative path to be rejected")
	}

	var invalidErr *EditorToolParameterInvalidError
	if !errors.As(err, &invalidErr) {
		t.Fatalf("validatePath() error = %T, want *EditorToolParameterInvalidError", err)
	}
	if invalidErr.Parameter != "path" {
		t.Fatalf("Parameter = %q, want path", invalidErr.Parameter)
	}
	expectedSuggestion := filepath.Clean(filepath.Join(cwd, input))
	if !strings.Contains(invalidErr.Hint, expectedSuggestion) {
		t.Fatalf("Hint = %q, want contains %q", invalidErr.Hint, expectedSuggestion)
	}
	if !strings.Contains(invalidErr.Hint, "绝对路径") {
		t.Fatalf("Hint = %q, want contains 绝对路径", invalidErr.Hint)
	}
}

func TestValidateFileEditorAction_SpecBoundaries(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		action FileEditorAction
	}{
		{
			name: "view range start greater than end",
			action: FileEditorAction{
				Command:   FileEditorCommandView,
				Path:      "/tmp/demo.txt",
				ViewRange: []int{20, 10},
			},
		},
		{
			name: "insert line is negative",
			action: FileEditorAction{
				Command:    FileEditorCommandInsert,
				Path:       "/tmp/demo.txt",
				InsertLine: intptr(-1),
				NewStr:     strptr("hello"),
			},
		},
		{
			name: "insert line is zero",
			action: FileEditorAction{
				Command:    FileEditorCommandInsert,
				Path:       "/tmp/demo.txt",
				InsertLine: intptr(0),
				NewStr:     strptr("hello"),
			},
		},
		{
			name: "replace old_str must not be empty",
			action: FileEditorAction{
				Command: FileEditorCommandStrReplace,
				Path:    "/tmp/demo.txt",
				OldStr:  strptr(""),
				NewStr:  strptr("world"),
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateFileEditorAction(tc.action)
			if err == nil {
				t.Fatal("expected validation error for boundary case, got nil")
			}
		})
	}
}

func TestFileEditorExecutor_StrReplaceSpec(t *testing.T) {
	t.Parallel()

	t.Run("old_str does not exist", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "replace.txt")
		writeTestFile(t, path, "hello world")

		obs := runFileEditorExecutor(t, FileEditorAction{
			Command: FileEditorCommandStrReplace,
			Path:    path,
			OldStr:  strptr("missing"),
			NewStr:  strptr("new"),
		})
		if obs == nil || !obs.IsError() {
			t.Fatal("expected not-found replace to return error observation")
		}
	})

	t.Run("old_str appears multiple times", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "replace.txt")
		writeTestFile(t, path, "x hello hello y")

		obs := runFileEditorExecutor(t, FileEditorAction{
			Command: FileEditorCommandStrReplace,
			Path:    path,
			OldStr:  strptr("hello"),
			NewStr:  strptr("world"),
		})
		if obs == nil || !obs.IsError() {
			t.Fatal("expected multi-match replace to return error observation")
		}
	})

	t.Run("old_str as first character", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "replace.txt")
		writeTestFile(t, path, "abc")

		obs := runFileEditorExecutor(t, FileEditorAction{
			Command: FileEditorCommandStrReplace,
			Path:    path,
			OldStr:  strptr("a"),
			NewStr:  strptr("z"),
		})
		if obs == nil || obs.IsError() {
			t.Fatalf("expected replace at first character to succeed, got content=%q", observationText(obs))
		}
	})

	t.Run("old_str as last character", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "replace.txt")
		writeTestFile(t, path, "abc")

		obs := runFileEditorExecutor(t, FileEditorAction{
			Command: FileEditorCommandStrReplace,
			Path:    path,
			OldStr:  strptr("c"),
			NewStr:  strptr("z"),
		})
		if obs == nil || obs.IsError() {
			t.Fatalf("expected replace at last character to succeed, got content=%q", observationText(obs))
		}
	})
}

func TestExecuteView_DirectoryListing(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "a.txt"), "alpha")
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	writeTestFile(t, filepath.Join(root, "docs", "readme.md"), "hello")
	writeTestFile(t, filepath.Join(root, ".secret"), "hidden")
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("创建隐藏目录失败: %v", err)
	}

	obs := runFileEditorExecutor(t, FileEditorAction{
		Command: FileEditorCommandView,
		Path:    root,
	})
	if obs == nil || obs.IsError() {
		t.Fatalf("expected directory view success, got content=%q", observationText(obs))
	}

	text := observationText(obs)
	if !strings.Contains(text, "这是两层深度的文件和文件夹列表") {
		t.Fatalf("directory view text = %q, want depth hint", text)
	}
	if !strings.Contains(text, "docs{}") {
		t.Fatalf("directory view text = %q, want contains docs{}", text)
	}
	if !strings.Contains(text, "readme.md") {
		t.Fatalf("directory view text = %q, want contains nested file", text)
	}
	if strings.Contains(text, ".secret") || strings.Contains(text, ".git") {
		t.Fatalf("directory view text = %q, hidden entries should be excluded", text)
	}
	if !strings.Contains(text, "已排除 2 个隐藏文件或文件夹") {
		t.Fatalf("directory view text = %q, want hidden count", text)
	}
}

func TestExecuteView_ImageFile(t *testing.T) {
	t.Parallel()

	const pngBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO7Z0WQAAAAASUVORK5CYII="

	root := t.TempDir()
	path := filepath.Join(root, "pixel.png")
	data, err := base64.StdEncoding.DecodeString(pngBase64)
	if err != nil {
		t.Fatalf("解码测试图片失败: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("写入测试图片失败: %v", err)
	}

	obs := runFileEditorExecutor(t, FileEditorAction{
		Command: FileEditorCommandView,
		Path:    path,
	})
	if obs == nil || obs.IsError() {
		t.Fatalf("expected image view success, got content=%q", observationText(obs))
	}

	text := observationText(obs)
	if !strings.Contains(text, "图片文件") {
		t.Fatalf("image view text = %q, want image success message", text)
	}

	imageURLs := observationImageURLs(obs)
	if len(imageURLs) != 1 {
		t.Fatalf("image url count = %d, want 1", len(imageURLs))
	}
	if !strings.HasPrefix(imageURLs[0], "data:image/png;base64,") {
		t.Fatalf("image url = %q, want data URI", imageURLs[0])
	}
}

func TestExecuteView_TextFileRangeAndLineNumbers(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "demo.txt")
	writeTestFile(t, path, "first\nsecond\nthird\nfourth\n")

	obs := runFileEditorExecutor(t, FileEditorAction{
		Command:   FileEditorCommandView,
		Path:      path,
		ViewRange: []int{2, 3},
	})
	if obs == nil || obs.IsError() {
		t.Fatalf("expected text view success, got content=%q", observationText(obs))
	}

	text := observationText(obs)
	if !strings.Contains(text, "第 2-3 行内容如下") {
		t.Fatalf("text view output = %q, want range header", text)
	}
	if !strings.Contains(text, "2| second") || !strings.Contains(text, "3| third") {
		t.Fatalf("text view output = %q, want numbered lines", text)
	}
	if strings.Contains(text, "1| first") {
		t.Fatalf("text view output = %q, should not contain line 1", text)
	}
}

func TestExecuteView_RejectBinaryFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "binary.bin")
	if err := os.WriteFile(path, []byte{0x00, 0x01, 0x02, 0x03}, 0o644); err != nil {
		t.Fatalf("写入二进制测试文件失败: %v", err)
	}

	obs := runFileEditorExecutor(t, FileEditorAction{
		Command: FileEditorCommandView,
		Path:    path,
	})
	if obs == nil || !obs.IsError() {
		t.Fatal("expected binary file view to return error observation")
	}
	if !strings.Contains(observationText(obs), "二进制文件") {
		t.Fatalf("binary view output = %q, want binary error", observationText(obs))
	}
}

func TestFileEditorExecutor_InsertAndViewSpec(t *testing.T) {
	t.Parallel()

	t.Run("insert line beyond file length", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "insert.txt")
		writeTestFile(t, path, "line1\nline2\n")

		obs := runFileEditorExecutor(t, FileEditorAction{
			Command:    FileEditorCommandInsert,
			Path:       path,
			InsertLine: intptr(100),
			NewStr:     strptr("line3"),
		})
		if obs == nil || !obs.IsError() {
			t.Fatal("expected insert beyond file length to return error observation")
		}
	})

	t.Run("view range start greater than end", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "view.txt")
		writeTestFile(t, path, "1\n2\n3\n")

		obs := runFileEditorExecutor(t, FileEditorAction{
			Command:   FileEditorCommandView,
			Path:      path,
			ViewRange: []int{20, 10},
		})
		if obs == nil || !obs.IsError() {
			t.Fatal("expected reversed view_range to return error observation")
		}
	})

	t.Run("insert into empty file", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "empty.txt")
		writeTestFile(t, path, "")

		obs := runFileEditorExecutor(t, FileEditorAction{
			Command:    FileEditorCommandInsert,
			Path:       path,
			InsertLine: intptr(1),
			NewStr:     strptr("hello"),
		})
		if obs == nil || obs.IsError() {
			t.Fatalf("expected insert into empty file to succeed, got content=%q", observationText(obs))
		}
	})
}

func TestFileEditorExecutor_UndoSpec(t *testing.T) {
	t.Parallel()

	t.Run("undo more than snapshot limit", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "undo.txt")
		writeTestFile(t, path, "v0")

		for i := range 10 {
			obs := runFileEditorExecutor(t, FileEditorAction{
				Command: FileEditorCommandUndoEdit,
				Path:    path,
			})
			if obs == nil {
				t.Fatalf("undo #%d returned nil observation", i+1)
			}
		}
	})

	t.Run("undo after file deleted externally", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "undo.txt")
		writeTestFile(t, path, "v1")
		if err := os.Remove(path); err != nil {
			t.Fatalf("删除测试文件失败: %v", err)
		}

		obs := runFileEditorExecutor(t, FileEditorAction{
			Command: FileEditorCommandUndoEdit,
			Path:    path,
		})
		if obs == nil || !obs.IsError() {
			t.Fatal("expected undo on deleted file to return error observation")
		}
	})
}

func TestFileEditorExecutor_OtherEdgeSpecs(t *testing.T) {
	t.Parallel()

	t.Run("view nonexistent file", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "missing.txt")
		obs := runFileEditorExecutor(t, FileEditorAction{
			Command: FileEditorCommandView,
			Path:    path,
		})
		if obs == nil || !obs.IsError() {
			t.Fatal("expected viewing nonexistent file to return error observation")
		}
	})

	t.Run("create should fail when file already exists", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "exists.txt")
		writeTestFile(t, path, "already here")

		obs := runFileEditorExecutor(t, FileEditorAction{
			Command:  FileEditorCommandCreate,
			Path:     path,
			FileText: strptr("new content"),
		})
		if obs == nil || !obs.IsError() {
			t.Fatal("expected create on existing file to return error observation")
		}
	})

	t.Run("create should fail when parent directory is missing", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "missing-parent", "new.txt")
		obs := runFileEditorExecutor(t, FileEditorAction{
			Command:  FileEditorCommandCreate,
			Path:     path,
			FileText: strptr("new content"),
		})
		if obs == nil || !obs.IsError() {
			t.Fatal("expected create with missing parent directory to return error observation")
		}
	})
}
