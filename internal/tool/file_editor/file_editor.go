package file_editor

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	defaultFileEditorHistoryMaxPerFile = 20
	defaultFileEditorCacheSizeLimit    = 32 * 1024 * 1024
	defaultFileEditorMaxFileSizeMB     = maxViewTextFileSizeBytes / 1024 / 1024
)

var (
	defaultFileEditorOnce sync.Once
	defaultFileEditor     *FileEditor
	defaultFileEditorErr  error
)

// FileEditor 表示文件编辑工具的运行时实例。
type FileEditor struct {
	MAX_FILE_SIZE_MB int64
	maxFileSize      int64
	historyManager   *fileHistoryManager
	cwd              string
}

// NewFileEditor 构造文件编辑工具实例。
func NewFileEditor(maxFileSizeMB int64, historyManager *fileHistoryManager, cwd string) (*FileEditor, error) {
	if maxFileSizeMB <= 0 {
		maxFileSizeMB = defaultFileEditorMaxFileSizeMB
	}
	if cwd == "" {
		resolvedCWD, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get current working directory: %w", err)
		}
		cwd = resolvedCWD
	}
	return &FileEditor{
		MAX_FILE_SIZE_MB: maxFileSizeMB,
		maxFileSize:      maxFileSizeMB * 1024 * 1024,
		historyManager:   historyManager,
		cwd:              cwd,
	}, nil
}

// NewDefaultFileEditor 构造默认文件编辑工具实例。
func NewDefaultFileEditor() (*FileEditor, error) {
	historyManager, err := newDefaultFileHistoryManager()
	if err != nil {
		return nil, err
	}
	return NewFileEditor(defaultFileEditorMaxFileSizeMB, historyManager, "")
}

// DefaultFileEditor 返回默认单例文件编辑工具实例。
func DefaultFileEditor() (*FileEditor, error) {
	defaultFileEditorOnce.Do(func() {
		defaultFileEditor, defaultFileEditorErr = NewDefaultFileEditor()
	})
	return defaultFileEditor, defaultFileEditorErr
}

// appendVersionToHistoryChain 将指定内容追加到文件版本链。
func (e *FileEditor) appendVersionToHistoryChain(filePath, content string) error {
	if e == nil {
		return fmt.Errorf("file editor is nil")
	}
	if e.historyManager == nil {
		return fmt.Errorf("file editor history manager is nil")
	}
	return e.historyManager.add(filePath, content)
}

// rollbackLatestVersionFromHistoryChain 回滚最近一次追加的版本链记录。
func (e *FileEditor) rollbackLatestVersionFromHistoryChain(filePath string) error {
	if e == nil {
		return fmt.Errorf("file editor is nil")
	}
	if e.historyManager == nil {
		return fmt.Errorf("file editor history manager is nil")
	}
	_, _, err := e.historyManager.pop(filePath)
	return err
}

// newDefaultFileHistoryManager 构造文件编辑工具默认使用的历史管理器。
func newDefaultFileHistoryManager() (*fileHistoryManager, error) {
	cacheDirectory := filepath.Join(os.TempDir(), "opentalon", "file_editor")
	cache, err := newFileCache(cacheDirectory, defaultFileEditorCacheSizeLimit)
	if err != nil {
		return nil, fmt.Errorf("create default file editor cache: %w", err)
	}
	return newFileHistoryManager(defaultFileEditorHistoryMaxPerFile, cache), nil
}
