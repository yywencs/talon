package file_editor

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/wen/opentalon/internal/types"
)

var supportedImageMIMEs = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// executeView 执行文件查看命令。
func (e *FileEditor) executeView(ctx context.Context, action FileEditorAction) *FileEditorObservation {
	_ = ctx

	info, err := os.Stat(action.Path)
	if err != nil {
		return NewErrorObservation(action.Command, action.Path, NewFileValidationError(action.Path, "路径不存在或不可访问", err))
	}

	if info.IsDir() {
		content, hiddenCount, buildErr := buildDirectoryViewContent(action.Path)
		if buildErr != nil {
			return NewErrorObservation(action.Command, action.Path, buildErr)
		}
		if hiddenCount > 0 {
			content += fmt.Sprintf("\n\n已排除 %d 个隐藏文件或文件夹，可通过 ls -la 查看。", hiddenCount)
		}
		return NewObservation(action.Command, action.Path, true, nil, nil, types.TextContent{Text: content})
	}

	if imageMIME, ok := detectSupportedImageMIME(action.Path); ok {
		return e.executeViewImage(action, info, imageMIME)
	}

	if info.Size() > e.maxFileSize {
		return NewErrorObservation(action.Command, action.Path, NewFileValidationError(action.Path, fmt.Sprintf("文件太大：%.2fMB，最大允许 %dMB", bytesToMB(info.Size()), e.MAX_FILE_SIZE_MB), nil))
	}

	text, fileEncoding, err := readTextFile(action.Path)
	if err != nil {
		return NewErrorObservation(action.Command, action.Path, NewFileValidationError(action.Path, fmt.Sprintf("读取文本文件失败: %v", err), err))
	}

	textContent, err := buildTextFileViewContent(action.Path, text, action.ViewRange)
	if err != nil {
		return NewErrorObservation(action.Command, action.Path, err)
	}
	if !fileEncoding.Editable && fileEncoding.Reason != "" {
		textContent = fmt.Sprintf("注意：文件编码为 %s，%s；当前仅允许查看，不允许编辑。\n\n%s", fileEncoding.Name, fileEncoding.Reason, textContent)
	}
	return NewObservation(action.Command, action.Path, true, nil, nil, types.TextContent{Text: textContent})
}

func (e *FileEditor) executeViewImage(action FileEditorAction, info os.FileInfo, imageMIME string) *FileEditorObservation {
	_ = e
	if info.Size() > maxViewImageFileSizeBytes {
		return NewErrorObservation(action.Command, action.Path, NewFileValidationError(action.Path, fmt.Sprintf("图片太大：%.2fMB，最大允许 %.2fMB", bytesToMB(info.Size()), bytesToMB(maxViewImageFileSizeBytes)), nil))
	}

	data, err := os.ReadFile(action.Path)
	if err != nil {
		return NewErrorObservation(action.Command, action.Path, NewFileValidationError(action.Path, "读取图片文件失败", err))
	}
	if !isDetectedImage(data) {
		return NewErrorObservation(action.Command, action.Path, NewFileValidationError(action.Path, "文件后缀是图片，但文件内容不是有效图像", nil))
	}

	imageURI := "data:" + imageMIME + ";base64," + base64.StdEncoding.EncodeToString(data)
	text := fmt.Sprintf("图片文件 %q 读取成功，图像内容如下。", action.Path)
	return NewObservation(
		action.Command,
		action.Path,
		true,
		nil,
		nil,
		types.TextContent{Text: text},
		types.ImageContent{ImageURLs: []string{imageURI}},
	)
}

func buildDirectoryViewContent(path string) (string, int, error) {
	lines := []string{
		fmt.Sprintf("这是两层深度的文件和文件夹列表：%s", path),
	}

	hiddenCount := 0
	if err := appendDirectoryEntries(path, 0, &lines, &hiddenCount); err != nil {
		return "", 0, NewFileValidationError(path, "读取目录内容失败", err)
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
		return "", NewFileValidationError(path, err.Error(), nil)
	}

	description := "全部内容如下"
	if len(viewRange) == 2 {
		description = fmt.Sprintf("第 %d-%d 行内容如下", selectedStart, selectedEnd)
	}
	return buildFilePreviewMessage(path, description, content, selectedStart, selectedEnd-selectedStart+1), nil
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

func isBinaryContent(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	sample := data[:minInt(len(data), 8192)]
	if _, ok := detectUTF16WithoutBOM(sample); ok {
		return false
	}
	if strings.IndexByte(string(sample), 0) >= 0 {
		return true
	}

	controlCount := 0
	for _, b := range sample {
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' {
			controlCount++
		}
	}
	return float64(controlCount)/float64(len(sample)) >= 0.1
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
