package file_editor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/wen/opentalon/pkg/logger"
)

type fileCache struct {
	directory   string
	sizeLimit   int64
	currentSize int64
}

type cacheEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type cacheFileMeta struct {
	path    string
	size    int64
	modTime int64
}

// newFileCache 初始化文件缓存目录，并根据现有文件重新计算当前占用空间。
func newFileCache(directory string, sizeLimit int64) (*fileCache, error) {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, fmt.Errorf("create cache directory %q: %w", directory, err)
	}

	currentSize := int64(0)
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read cache directory %q: %w", directory, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("read cache file info %q: %w", filepath.Join(directory, entry.Name()), err)
		}
		currentSize += info.Size()
	}

	cache := &fileCache{
		directory:   directory,
		sizeLimit:   sizeLimit,
		currentSize: currentSize,
	}
	logger.Debug("文件缓存已初始化",
		"directory", directory,
		"size_limit", sizeLimit,
		"current_size", currentSize,
	)
	return cache, nil
}

func (c *fileCache) get(key string) (string, bool, error) {
	if c == nil {
		return "", false, errors.New("fileCache is nil")
	}

	path := c.cacheFilePath(key)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read cache file for key %q at %q: %w", key, path, err)
	}

	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return "", false, fmt.Errorf("unmarshal cache entry for key %q at %q: %w", key, path, err)
	}
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		logger.Warn("文件缓存访问时间更新失败",
			"file_path", path,
			"error", err,
		)
	}
	return entry.Value, true, nil
}

func (c *fileCache) set(key, value string) error {
	if c == nil {
		return errors.New("fileCache is nil")
	}

	path := c.cacheFilePath(key)
	oldSize := int64(0)
	if info, err := os.Stat(path); err == nil {
		oldSize = info.Size()
	} else if errors.Is(err, os.ErrNotExist) {
		// 文件不存在时按新建处理，后续由 os.WriteFile 创建缓存文件。
		oldSize = 0
	} else {
		return fmt.Errorf("stat cache file for key %q at %q: %w", key, path, err)
	}

	entry := cacheEntry{
		Key:   key,
		Value: value,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal cache entry for key %q: %w", key, err)
	}

	newSize := int64(len(data))
	projectedSize := c.currentSize - oldSize + newSize
	if c.sizeLimit > 0 && projectedSize > c.sizeLimit {
		if err := c.evictLRU(projectedSize-c.sizeLimit, path); err != nil {
			return fmt.Errorf("evict LRU cache files before writing key %q: %w", key, err)
		}
		projectedSize = c.currentSize - oldSize + newSize
		if projectedSize > c.sizeLimit {
			return fmt.Errorf("cache entry for key %q exceeds size limit after LRU eviction: projected_size=%d size_limit=%d", key, projectedSize, c.sizeLimit)
		}
	}

	if err := writeFileAtomically(path, data, 0o644); err != nil {
		return fmt.Errorf("write cache file for key %q at %q: %w", key, path, err)
	}

	c.currentSize = projectedSize
	return nil
}

func (c *fileCache) delete(key string) error {
	if c == nil {
		return errors.New("fileCache is nil")
	}

	path := c.cacheFilePath(key)
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat cache file for delete key %q at %q: %w", key, path, err)
	}

	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove cache file for key %q at %q: %w", key, path, err)
	}

	c.currentSize -= info.Size()
	if c.currentSize < 0 {
		c.currentSize = 0
	}
	return nil
}

func (c *fileCache) clear() error {
	if c == nil {
		return errors.New("fileCache is nil")
	}

	files, err := c.listCacheFiles()
	if err != nil {
		return fmt.Errorf("list cache files for clear in %q: %w", c.directory, err)
	}
	for _, file := range files {
		if err := os.Remove(file.path); err != nil {
			return fmt.Errorf("remove cache file during clear %q: %w", file.path, err)
		}
	}
	c.currentSize = 0
	return nil
}

func (c *fileCache) cacheFilePath(key string) string {
	sum := sha256.Sum256([]byte(key))
	filename := hex.EncodeToString(sum[:]) + ".json"
	return filepath.Join(c.directory, filename)
}

func (c *fileCache) evictLRU(requiredFreeSize int64, excludePath string) error {
	files, err := c.listCacheFiles()
	if err != nil {
		return fmt.Errorf("list cache files for LRU eviction in %q: %w", c.directory, err)
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime < files[j].modTime
	})

	freedSize := int64(0)
	for _, file := range files {
		if file.path == excludePath {
			continue
		}
		if err := os.Remove(file.path); err != nil {
			logger.Warn("文件缓存按 LRU 淘汰旧文件失败，已跳过",
				"file_path", file.path,
				"error", err,
			)
			continue
		}
		c.currentSize -= file.size
		if c.currentSize < 0 {
			c.currentSize = 0
		}
		freedSize += file.size
		logger.Debug("文件缓存已按 LRU 淘汰旧文件",
			"file_path", file.path,
			"freed_size", file.size,
			"current_size", c.currentSize,
			"size_limit", c.sizeLimit,
		)
		if freedSize >= requiredFreeSize {
			return nil
		}
	}
	return nil
}

func (c *fileCache) listCacheFiles() ([]cacheFileMeta, error) {
	entries, err := os.ReadDir(c.directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read cache directory %q for file listing: %w", c.directory, err)
	}

	files := make([]cacheFileMeta, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("read cache file info %q during listing: %w", filepath.Join(c.directory, entry.Name()), err)
		}
		files = append(files, cacheFileMeta{
			path:    filepath.Join(c.directory, entry.Name()),
			size:    info.Size(),
			modTime: info.ModTime().UnixNano(),
		})
	}
	return files, nil
}

func writeFileAtomically(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tempFile, err := os.CreateTemp(dir, ".tmp-cache-*")
	if err != nil {
		return fmt.Errorf("create temp file in %q: %w", dir, err)
	}

	tempPath := tempFile.Name()
	cleanupTempFile := true
	defer func() {
		if cleanupTempFile {
			_ = os.Remove(tempPath)
		}
	}()

	if err := tempFile.Chmod(perm); err != nil {
		_ = tempFile.Close()
		return fmt.Errorf("chmod temp file %q: %w", tempPath, err)
	}
	if _, err := tempFile.Write(data); err != nil {
		_ = tempFile.Close()
		return fmt.Errorf("write temp file %q: %w", tempPath, err)
	}
	if err := tempFile.Sync(); err != nil {
		_ = tempFile.Close()
		return fmt.Errorf("sync temp file %q: %w", tempPath, err)
	}
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("close temp file %q: %w", tempPath, err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("rename temp file %q to %q: %w", tempPath, path, err)
	}

	cleanupTempFile = false
	return nil
}
