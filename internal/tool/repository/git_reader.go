package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	fullSHAPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
	symbolPattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]{0,127}$`)
)

// GitConfig 将一个本地 Git 仓库绑定到本次 Review 的两个可信 Commit。
type GitConfig struct {
	RepositoryRoot string
	BaseSHA        string
	HeadSHA        string
	Limits         Limits
}

// GitReader 只执行 cat-file、grep 和 ls-tree，不 checkout、不写工作区，也不运行 Hook。
type GitReader struct {
	root      string
	revisions map[Revision]string
	limits    Limits
}

// NewGitReader 校验仓库和两个 Commit 是否已离线存在，然后创建只读 Reader。
func NewGitReader(ctx context.Context, config GitConfig) (*GitReader, error) {
	root, err := filepath.Abs(strings.TrimSpace(config.RepositoryRoot))
	if err != nil {
		return nil, fmt.Errorf("repository: resolve repository root: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("repository: stat repository root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("repository: repository root is not a directory")
	}
	if !fullSHAPattern.MatchString(config.BaseSHA) || !fullSHAPattern.MatchString(config.HeadSHA) {
		return nil, fmt.Errorf("repository: base and head must be full 40-character commit SHAs")
	}
	reader := &GitReader{
		root: root,
		revisions: map[Revision]string{
			RevisionBase: strings.ToLower(config.BaseSHA),
			RevisionHead: strings.ToLower(config.HeadSHA),
		},
		limits: normalizeLimits(config.Limits),
	}
	inside, err := reader.git(ctx, 128, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(string(inside)) != "true" {
		return nil, fmt.Errorf("repository: repository root is not a Git work tree")
	}
	for _, revision := range []Revision{RevisionBase, RevisionHead} {
		sha, _ := reader.resolveRevision(revision)
		if _, err := reader.git(ctx, 128, "cat-file", "-e", sha+"^{commit}"); err != nil {
			return nil, fmt.Errorf("repository: %s commit is unavailable offline: %w", revision, err)
		}
	}
	return reader, nil
}

// ReadFile 读取 Git 对象中的普通文本文件，并按请求范围添加行号。
func (r *GitReader) ReadFile(ctx context.Context, revision Revision, filePath string, startLine, endLine int) (FileSnippet, error) {
	sha, err := r.resolveRevision(revision)
	if err != nil {
		return FileSnippet{}, err
	}
	filePath, err = validateRepositoryPath(filePath, false)
	if err != nil {
		return FileSnippet{}, err
	}
	if startLine <= 0 || endLine < startLine || endLine-startLine+1 > r.limits.MaxReadLines {
		return FileSnippet{}, fmt.Errorf("repository: requested line range must contain 1-%d lines", r.limits.MaxReadLines)
	}
	objectSpec := sha + ":" + filePath
	entry, err := r.git(ctx, 4096, "ls-tree", "-z", sha, "--", filePath)
	if err != nil {
		return FileSnippet{}, err
	}
	mode, objectType, listedPath, err := parseExactTreeEntry(entry)
	if err != nil {
		return FileSnippet{}, err
	}
	if listedPath != filePath || mode == "120000" || objectType != "blob" {
		return FileSnippet{}, fmt.Errorf("repository: %q is not a regular file at %s", filePath, revision)
	}
	sizeOutput, err := r.git(ctx, 128, "cat-file", "-s", objectSpec)
	if err != nil {
		return FileSnippet{}, err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(sizeOutput)), 10, 64)
	if err != nil {
		return FileSnippet{}, fmt.Errorf("repository: parse file size: %w", err)
	}
	if size > r.limits.MaxFileBytes {
		return FileSnippet{}, fmt.Errorf("repository: file %q exceeds %d-byte limit", filePath, r.limits.MaxFileBytes)
	}
	content, err := r.git(ctx, r.limits.MaxFileBytes+1, "cat-file", "blob", objectSpec)
	if err != nil {
		return FileSnippet{}, err
	}
	if bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content) {
		return FileSnippet{}, fmt.Errorf("repository: file %q is not UTF-8 text", filePath)
	}
	lines := splitLines(content)
	if startLine > len(lines) {
		return FileSnippet{}, fmt.Errorf("repository: start line %d exceeds file length %d", startLine, len(lines))
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	var rendered strings.Builder
	for lineNumber := startLine; lineNumber <= endLine; lineNumber++ {
		fmt.Fprintf(&rendered, "%d | %s\n", lineNumber, lines[lineNumber-1])
	}
	return FileSnippet{
		Revision: revision, Path: filePath, StartLine: startLine, EndLine: endLine,
		Content: strings.TrimSuffix(rendered.String(), "\n"),
	}, nil
}

// SearchSymbol 在 Go 文件中执行固定字符串搜索，不接受正则表达式或任意 pathspec。
func (r *GitReader) SearchSymbol(ctx context.Context, revision Revision, symbol string) ([]SearchResult, error) {
	sha, err := r.resolveRevision(revision)
	if err != nil {
		return nil, err
	}
	if !symbolPattern.MatchString(symbol) {
		return nil, fmt.Errorf("repository: symbol must be a Go-like identifier with at most 128 characters")
	}
	output, err := r.git(ctx, r.limits.MaxCommandBytes, "grep", "-n", "-z", "-F", "-I", "-e", symbol, sha, "--", "*.go")
	if err != nil {
		var commandError *gitCommandError
		if errors.As(err, &commandError) && commandError.ExitCode == 1 {
			return make([]SearchResult, 0), nil
		}
		return nil, err
	}
	if !utf8.Valid(output) {
		return nil, fmt.Errorf("repository: git grep returned non-UTF-8 output")
	}
	results, err := parseGrepResults(output, sha, r.limits.MaxSearchResult)
	if err != nil {
		return nil, err
	}
	return results, nil
}

// ListFiles 列出指定目录下的仓库文件，返回值有数量上限且顺序稳定。
func (r *GitReader) ListFiles(ctx context.Context, revision Revision, directory string) ([]string, error) {
	sha, err := r.resolveRevision(revision)
	if err != nil {
		return nil, err
	}
	directory, err = validateRepositoryPath(directory, true)
	if err != nil {
		return nil, err
	}
	args := []string{"ls-tree", "-r", "-z", "--name-only", sha, "--"}
	if directory != "." {
		args = append(args, directory)
	}
	output, err := r.git(ctx, r.limits.MaxCommandBytes, args...)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(output) {
		return nil, fmt.Errorf("repository: git ls-tree returned non-UTF-8 paths")
	}
	parts := bytes.Split(output, []byte{0})
	files := make([]string, 0, min(len(parts), r.limits.MaxListedFiles))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		if len(files) == r.limits.MaxListedFiles {
			break
		}
		files = append(files, string(part))
	}
	sort.Strings(files)
	return files, nil
}

func (r *GitReader) resolveRevision(revision Revision) (string, error) {
	sha, exists := r.revisions[revision]
	if !exists {
		return "", fmt.Errorf("repository: revision must be %q or %q", RevisionBase, RevisionHead)
	}
	return sha, nil
}

type gitCommandError struct {
	ExitCode int
	Stderr   string
}

func (e *gitCommandError) Error() string {
	if e.Stderr == "" {
		return fmt.Sprintf("repository: git exited with code %d", e.ExitCode)
	}
	return fmt.Sprintf("repository: git exited with code %d: %s", e.ExitCode, e.Stderr)
}

func (r *GitReader) git(ctx context.Context, maxBytes int64, args ...string) ([]byte, error) {
	commandArgs := append([]string{"--no-optional-locks", "-C", r.root}, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	command.Env = append(os.Environ(),
		"GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "LC_ALL=C",
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("repository: open git stdout: %w", err)
	}
	var stderr cappedBuffer
	stderr.max = 4096
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("repository: start git: %w", err)
	}
	output, readErr := io.ReadAll(io.LimitReader(stdout, maxBytes+1))
	if int64(len(output)) > maxBytes {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, fmt.Errorf("repository: git output exceeds %d-byte limit", maxBytes)
	}
	waitErr := command.Wait()
	if readErr != nil {
		return nil, fmt.Errorf("repository: read git output: %w", readErr)
	}
	if waitErr != nil {
		exitCode := -1
		var exitError *exec.ExitError
		if errors.As(waitErr, &exitError) {
			exitCode = exitError.ExitCode()
		}
		return output, &gitCommandError{ExitCode: exitCode, Stderr: strings.TrimSpace(stderr.String())}
	}
	return output, nil
}

type cappedBuffer struct {
	bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := b.max - b.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = b.Buffer.Write(data)
	}
	return original, nil
}

func normalizeLimits(limits Limits) Limits {
	defaults := defaultLimits()
	if limits.MaxFileBytes <= 0 {
		limits.MaxFileBytes = defaults.MaxFileBytes
	}
	if limits.MaxReadLines <= 0 {
		limits.MaxReadLines = defaults.MaxReadLines
	}
	if limits.MaxSearchResult <= 0 {
		limits.MaxSearchResult = defaults.MaxSearchResult
	}
	if limits.MaxListedFiles <= 0 {
		limits.MaxListedFiles = defaults.MaxListedFiles
	}
	if limits.MaxCommandBytes <= 0 {
		limits.MaxCommandBytes = defaults.MaxCommandBytes
	}
	return limits
}

func validateRepositoryPath(raw string, allowRoot bool) (string, error) {
	if strings.ContainsRune(raw, '\\') || strings.ContainsRune(raw, 0) {
		return "", fmt.Errorf("repository: repository path contains forbidden characters")
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed != raw {
		return "", fmt.Errorf("repository: repository path has leading or trailing whitespace")
	}
	raw = trimmed
	if len(raw) > 4096 || !utf8.ValidString(raw) {
		return "", fmt.Errorf("repository: repository path is too long or not UTF-8")
	}
	for _, value := range raw {
		if unicode.IsControl(value) {
			return "", fmt.Errorf("repository: repository path contains control characters")
		}
	}
	if raw == "" && allowRoot {
		return ".", nil
	}
	if raw == "" || path.IsAbs(raw) {
		return "", fmt.Errorf("repository: repository path must be relative")
	}
	for _, segment := range strings.Split(raw, "/") {
		if segment == ".." {
			return "", fmt.Errorf("repository: repository path escapes the root")
		}
	}
	cleaned := path.Clean(raw)
	if cleaned == "." {
		if allowRoot {
			return cleaned, nil
		}
		return "", fmt.Errorf("repository: file path is required")
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("repository: repository path escapes the root")
	}
	for _, segment := range strings.Split(cleaned, "/") {
		if segment == ".git" {
			return "", fmt.Errorf("repository: access to .git paths is forbidden")
		}
	}
	return cleaned, nil
}

func parseExactTreeEntry(output []byte) (mode, objectType, filePath string, err error) {
	output = bytes.TrimSuffix(output, []byte{0})
	if len(output) == 0 {
		return "", "", "", fmt.Errorf("repository: file does not exist at requested revision")
	}
	if bytes.Contains(output, []byte{0}) {
		return "", "", "", fmt.Errorf("repository: path did not resolve to exactly one tree entry")
	}
	metadata, name, found := bytes.Cut(output, []byte{'\t'})
	if !found {
		return "", "", "", fmt.Errorf("repository: malformed git tree entry")
	}
	fields := strings.Fields(string(metadata))
	if len(fields) != 3 {
		return "", "", "", fmt.Errorf("repository: malformed git tree metadata")
	}
	return fields[0], fields[1], string(name), nil
}

func parseGrepResults(output []byte, sha string, limit int) ([]SearchResult, error) {
	prefix := sha + ":"
	results := make([]SearchResult, 0, min(limit, 16))
	for len(output) > 0 && len(results) < limit {
		fileEnd := bytes.IndexByte(output, 0)
		if fileEnd < 0 {
			return nil, fmt.Errorf("repository: malformed git grep filename")
		}
		filePath := strings.TrimPrefix(string(output[:fileEnd]), prefix)
		if filePath == string(output[:fileEnd]) {
			return nil, fmt.Errorf("repository: git grep result is outside the requested revision")
		}
		output = output[fileEnd+1:]
		lineEnd := bytes.IndexByte(output, 0)
		if lineEnd < 0 {
			return nil, fmt.Errorf("repository: malformed git grep line number")
		}
		lineNumber, err := strconv.Atoi(string(output[:lineEnd]))
		if err != nil {
			return nil, fmt.Errorf("repository: parse git grep line number: %w", err)
		}
		output = output[lineEnd+1:]
		contentEnd := bytes.IndexByte(output, '\n')
		if contentEnd < 0 {
			contentEnd = len(output)
		}
		content := strings.TrimSpace(string(output[:contentEnd]))
		if contentEnd == len(output) {
			output = nil
		} else {
			output = output[contentEnd+1:]
		}
		results = append(results, SearchResult{Path: filePath, Line: lineNumber, Content: content})
	}
	return results, nil
}

func splitLines(content []byte) []string {
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return []string{""}
	}
	return strings.Split(text, "\n")
}
