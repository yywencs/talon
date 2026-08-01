package review

import (
	"bufio"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var hunkHeaderPattern = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(?: ?.*)?$`)

// ParseUnifiedDiff 将 git 风格的 unified diff 解析为文件、Hunk 和精确行号。
// 解析过程只处理传入文本，不访问工作区，因而不会让不可信 Diff 触发文件读写或命令执行。
func ParseUnifiedDiff(raw string) ([]ChangedFile, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("review: diff is empty")
	}

	var files []ChangedFile
	var current *ChangedFile
	var currentHunk *Hunk
	oldLine, newLine := 0, 0

	// 解析器按“文件 -> Hunk -> 行”逐级推进；遇到下一层同级节点时，先提交当前节点。
	finishHunk := func() {
		if current == nil || currentHunk == nil {
			return
		}
		current.Hunks = append(current.Hunks, *currentHunk)
		currentHunk = nil
	}
	finishFile := func() {
		if current == nil {
			return
		}
		finishHunk()
		if current.OldPath != "" || current.NewPath != "" || len(current.Hunks) > 0 {
			current.Status = fileStatus(current.OldPath, current.NewPath)
			files = append(files, *current)
		}
		current = nil
	}

	// 默认 Scanner 只允许 64 KiB token。这里放宽单行上限，以兼容压缩代码或生成文件；
	// 整体 Diff 大小仍由入口层的 max-diff-bytes 限制。
	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")

		// diff --git 标志着新文件开始，同时也能为 rename/binary diff 提供基础路径。
		if strings.HasPrefix(line, "diff --git ") {
			finishFile()
			current = &ChangedFile{}
			oldPath, newPath := parseGitHeaderPaths(strings.TrimPrefix(line, "diff --git "))
			current.OldPath = normalizeDiffPath(oldPath, "a/")
			current.NewPath = normalizeDiffPath(newPath, "b/")
			continue
		}

		// ---/+++ 是旧、新文件路径；/dev/null 分别表示新增文件或删除文件。
		if strings.HasPrefix(line, "--- ") {
			if current == nil {
				current = &ChangedFile{}
			} else if currentHunk != nil || len(current.Hunks) > 0 {
				finishFile()
				current = &ChangedFile{}
			}
			current.OldPath = normalizeDiffPath(parseMarkerPath(strings.TrimPrefix(line, "--- ")), "a/")
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			if current == nil {
				return nil, fmt.Errorf("review: new file marker appears before old file marker")
			}
			current.NewPath = normalizeDiffPath(parseMarkerPath(strings.TrimPrefix(line, "+++ ")), "b/")
			continue
		}

		// Hunk 头部同时给出新旧文件的起始行和覆盖行数，后续逐行更新两个游标。
		if strings.HasPrefix(line, "@@ ") {
			if current == nil {
				return nil, fmt.Errorf("review: hunk appears before file header")
			}
			finishHunk()
			matches := hunkHeaderPattern.FindStringSubmatch(line)
			if matches == nil {
				return nil, fmt.Errorf("review: malformed hunk header %q", line)
			}
			oldStart, _ := strconv.Atoi(matches[1])
			newStart, _ := strconv.Atoi(matches[3])
			currentHunk = &Hunk{
				Header:   line,
				OldStart: oldStart,
				OldCount: parseCount(matches[2]),
				NewStart: newStart,
				NewCount: parseCount(matches[4]),
				Lines:    make([]ChangedLine, 0),
			}
			oldLine, newLine = oldStart, newStart
			continue
		}

		if currentHunk == nil || line == `\ No newline at end of file` {
			continue
		}
		if line == "" {
			return nil, fmt.Errorf("review: malformed empty line inside hunk %q", currentHunk.Header)
		}

		// 上下文行同时推进新旧游标；新增、删除行只推进各自一侧的游标。
		switch line[0] {
		case ' ':
			currentHunk.Lines = append(currentHunk.Lines, ChangedLine{
				Kind: LineContext, OldLine: oldLine, NewLine: newLine, Content: line[1:],
			})
			oldLine++
			newLine++
		case '+':
			currentHunk.Lines = append(currentHunk.Lines, ChangedLine{
				Kind: LineAdded, NewLine: newLine, Content: line[1:],
			})
			newLine++
		case '-':
			currentHunk.Lines = append(currentHunk.Lines, ChangedLine{
				Kind: LineRemoved, OldLine: oldLine, Content: line[1:],
			})
			oldLine++
		default:
			return nil, fmt.Errorf("review: malformed hunk line %q", line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("review: scan diff: %w", err)
	}
	finishFile()
	if len(files) == 0 {
		return nil, fmt.Errorf("review: no file changes found in unified diff")
	}
	return files, nil
}

func parseCount(raw string) int {
	// unified diff 省略 count 时语义为 1，例如 @@ -3 +3 @@。
	if raw == "" {
		return 1
	}
	count, _ := strconv.Atoi(raw)
	return count
}

func fileStatus(oldPath, newPath string) FileStatus {
	switch {
	case oldPath == "" && newPath != "":
		return FileAdded
	case oldPath != "" && newPath == "":
		return FileDeleted
	default:
		return FileModified
	}
}

func parseMarkerPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Git 会使用 C 风格引号转义包含空格或特殊字符的路径。
	if strings.HasPrefix(raw, `"`) {
		if path, ok := consumeQuotedPath(raw); ok {
			return path
		}
	}
	if index := strings.IndexByte(raw, '\t'); index >= 0 {
		return raw[:index]
	}
	return raw
}

func parseGitHeaderPaths(raw string) (string, string) {
	first, rest, ok := consumePathToken(strings.TrimSpace(raw))
	if !ok {
		return "", ""
	}
	second, _, ok := consumePathToken(strings.TrimSpace(rest))
	if !ok {
		return first, ""
	}
	return first, second
}

func consumePathToken(raw string) (token, rest string, ok bool) {
	if raw == "" {
		return "", "", false
	}
	if raw[0] == '"' {
		for i := 1; i < len(raw); i++ {
			if raw[i] != '"' || isEscaped(raw, i) {
				continue
			}
			value, err := strconv.Unquote(raw[:i+1])
			if err != nil {
				return "", "", false
			}
			return value, raw[i+1:], true
		}
		return "", "", false
	}
	if index := strings.IndexAny(raw, " \t"); index >= 0 {
		return raw[:index], raw[index:], true
	}
	return raw, "", true
}

func consumeQuotedPath(raw string) (string, bool) {
	value, _, ok := consumePathToken(raw)
	return value, ok
}

func isEscaped(raw string, index int) bool {
	backslashes := 0
	for i := index - 1; i >= 0 && raw[i] == '\\'; i-- {
		backslashes++
	}
	return backslashes%2 == 1
}

func normalizeDiffPath(path, prefix string) string {
	path = strings.TrimSpace(path)
	if path == "" || path == "/dev/null" {
		return ""
	}
	// 输出仓库相对路径，避免 Finding 中混入 Git 人工添加的 a/、b/ 前缀。
	return strings.TrimPrefix(path, prefix)
}
