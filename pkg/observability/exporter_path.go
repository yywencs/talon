package observability

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// traceDirectoryManager 管理追踪文件的目录结构。
// 将相同 traceID 的 Span 聚合到同一目录，并按进程分组。
type traceDirectoryManager struct {
	mu         sync.Mutex
	processDir string
	traceDirs  map[string]string
}

// newTraceDirectoryManager 创建目录管理器，返回进程级别的追踪目录。
func newTraceDirectoryManager(baseDir string) (*traceDirectoryManager, error) {
	processDir, err := buildProcessDirectory(baseDir)
	if err != nil {
		return nil, err
	}
	return &traceDirectoryManager{
		processDir: processDir,
		traceDirs:  make(map[string]string),
	}, nil
}

// ProcessDir 返回进程级别的追踪目录路径。
func (m *traceDirectoryManager) ProcessDir() string {
	if m == nil {
		return ""
	}
	return m.processDir
}

// TraceDir 根据 spanRecord 的 traceID 返回对应的追踪目录。
// 同一 traceID 的多次调用返回相同目录，新建 traceID 时自动创建目录。
func (m *traceDirectoryManager) TraceDir(record spanRecord) (string, error) {
	if m == nil {
		return "", fmt.Errorf("trace directory manager is nil")
	}
	traceID := strings.TrimSpace(record.TraceID)
	if traceID == "" {
		return "", fmt.Errorf("trace_id is empty")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if dir, ok := m.traceDirs[traceID]; ok {
		return dir, nil
	}

	traceDir := filepath.Join(m.processDir, buildTraceDirectoryName(record))
	if err := os.MkdirAll(traceDir, 0755); err != nil {
		return "", fmt.Errorf("create trace dir: %w", err)
	}
	m.traceDirs[traceID] = traceDir
	return traceDir, nil
}

// buildProcessDirectory 创建以时间戳和进程 ID 命名的目录。
func buildProcessDirectory(baseDir string) (string, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return "", fmt.Errorf("create trace root dir: %w", err)
	}
	dirName := fmt.Sprintf("%s-%d", formatTimestampForPath(time.Now()), os.Getpid())
	processDir := filepath.Join(baseDir, dirName)
	if err := os.MkdirAll(processDir, 0755); err != nil {
		return "", fmt.Errorf("create process trace dir: %w", err)
	}
	return processDir, nil
}

// buildTraceDirectoryName 根据时间戳和 traceID 构建目录名。
func buildTraceDirectoryName(record spanRecord) string {
	startTime := record.StartTime
	if startTime.IsZero() {
		startTime = time.Now()
	}
	return fmt.Sprintf("%s-%s", formatTimestampForPath(startTime), record.TraceID)
}

// buildSpanFileName 为 Span 构建文件名。
func buildSpanFileName(record spanRecord) string {
	startTime := record.StartTime
	if startTime.IsZero() {
		startTime = time.Now()
	}
	parts := []string{
		formatTimestampForPath(startTime),
		record.SpanID,
		sanitizePathToken(record.Name),
	}
	return strings.Join(parts, "-") + ".json"
}

// formatTimestampForPath 将时间转换为路径安全的字符串格式。
func formatTimestampForPath(ts time.Time) string {
	return ts.UTC().Format("20060102T150405.000000000Z")
}

// sanitizePathToken 清理字符串中的非法路径字符。
func sanitizePathToken(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "span"
	}
	replacer := strings.NewReplacer(
		"/", "-",
		"\\", "-",
		" ", "-",
		":", "-",
		"*", "-",
		"?", "-",
		"\"", "-",
		"<", "-",
		">", "-",
		"|", "-",
	)
	value = replacer.Replace(value)
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == '-'
	})
	if len(parts) == 0 {
		return "span"
	}
	value = strings.Join(parts, "-")
	if value == "" {
		return "span"
	}
	return value
}
