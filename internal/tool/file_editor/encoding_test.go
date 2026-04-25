package file_editor

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFileBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("写入测试二进制文件失败: %v", err)
	}
}

func TestReadTextFile_GB18030(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "gb18030.txt")
	content := "你好，世界\n第二行\n"
	data, err := encodeGB18030String(content)
	if err != nil {
		t.Fatalf("编码 GB18030 测试数据失败: %v", err)
	}
	writeTestFileBytes(t, path, data)

	got, fileEncoding, err := readTextFile(path)
	if err != nil {
		t.Fatalf("readTextFile() error = %v", err)
	}
	if got != content {
		t.Fatalf("readTextFile() content = %q, want %q", got, content)
	}
	if fileEncoding.Kind != textEncodingGB18030 {
		t.Fatalf("encoding kind = %q, want %q", fileEncoding.Kind, textEncodingGB18030)
	}
	if !fileEncoding.Editable {
		t.Fatal("GB18030 文件应允许编辑")
	}
}

func TestFileEditorEncoding_UTF16WithoutBOMViewOnly(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "utf16le-no-bom.txt")
	writeTestFileBytes(t, path, encodeUTF16("hello\nworld\n", binary.LittleEndian, false))

	viewObs := runFileEditorExecutor(t, FileEditorAction{
		Command: FileEditorCommandView,
		Path:    path,
	})
	if viewObs == nil || viewObs.IsError() {
		t.Fatalf("expected UTF-16 no BOM view success, got content=%q", observationText(viewObs))
	}
	viewText := observationText(viewObs)
	if !strings.Contains(viewText, "仅允许查看，不允许编辑") {
		t.Fatalf("view output = %q, want readonly hint", viewText)
	}
	if !strings.Contains(viewText, "1| hello") || !strings.Contains(viewText, "2| world") {
		t.Fatalf("view output = %q, want decoded text lines", viewText)
	}

	replaceObs := runFileEditorExecutor(t, FileEditorAction{
		Command: FileEditorCommandStrReplace,
		Path:    path,
		OldStr:  strptr("hello"),
		NewStr:  strptr("hola"),
	})
	if replaceObs == nil || !replaceObs.IsError() {
		t.Fatal("expected UTF-16 no BOM replace to be rejected")
	}
	if !strings.Contains(observationText(replaceObs), "仅允许查看，不允许编辑") {
		t.Fatalf("replace output = %q, want readonly rejection", observationText(replaceObs))
	}
}

func TestFileEditorEncoding_StrReplacePreservesGB18030(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "replace-gb18030.txt")
	oldContent := "你好，世界\n"
	initialData, err := encodeGB18030String(oldContent)
	if err != nil {
		t.Fatalf("编码初始 GB18030 数据失败: %v", err)
	}
	writeTestFileBytes(t, path, initialData)

	obs := runFileEditorExecutor(t, FileEditorAction{
		Command: FileEditorCommandStrReplace,
		Path:    path,
		OldStr:  strptr("世界"),
		NewStr:  strptr("朋友"),
	})
	if obs == nil || obs.IsError() {
		t.Fatalf("expected GB18030 replace success, got content=%q", observationText(obs))
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取替换后文件失败: %v", err)
	}
	expectedData, err := encodeGB18030String("你好，朋友\n")
	if err != nil {
		t.Fatalf("编码期望 GB18030 数据失败: %v", err)
	}
	if string(raw) == "你好，朋友\n" {
		t.Fatal("文件不应被直接回写为 UTF-8 字节")
	}
	if !bytes.Equal(raw, expectedData) {
		t.Fatalf("替换后字节不匹配 GB18030 期望结果")
	}
}
