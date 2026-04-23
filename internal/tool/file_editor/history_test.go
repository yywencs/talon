package file_editor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newTestFileCache(t *testing.T) *fileCache {
	t.Helper()
	return &fileCache{
		directory: filepath.Join(t.TempDir(), "cache"),
		sizeLimit: 1024,
	}
}

func expectedCachePath(dir, key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".json")
}

func readCacheEntryFile(t *testing.T, path string) cacheEntry {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取缓存文件失败: %v", err)
	}

	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("解析缓存文件 JSON 失败: %v", err)
	}
	return entry
}

func TestFileCacheSetAndGet(t *testing.T) {
	t.Parallel()

	cache := newTestFileCache(t)
	key := "/tmp/demo.txt"
	value := "hello world"

	if err := cache.set(key, value); err != nil {
		t.Fatalf("set() error = %v", err)
	}

	got, ok, err := cache.get(key)
	if err != nil {
		t.Fatalf("get() error = %v", err)
	}
	if !ok {
		t.Fatal("get() ok = false, want true")
	}
	if got != value {
		t.Fatalf("get() value = %q, want %q", got, value)
	}
}

func TestFileCacheSetStoresHashedJSONFile(t *testing.T) {
	t.Parallel()

	cache := newTestFileCache(t)
	key := "/Users/wen/demo.txt"
	value := "content"

	if err := cache.set(key, value); err != nil {
		t.Fatalf("set() error = %v", err)
	}

	cachePath := expectedCachePath(cache.directory, key)
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("缓存文件不存在: %v", err)
	}

	entry := readCacheEntryFile(t, cachePath)
	if entry.Key != key {
		t.Fatalf("entry.Key = %q, want %q", entry.Key, key)
	}
	if entry.Value != value {
		t.Fatalf("entry.Value = %q, want %q", entry.Value, value)
	}
}

func TestFileCacheGetMissingKey(t *testing.T) {
	t.Parallel()

	cache := newTestFileCache(t)

	got, ok, err := cache.get("/tmp/missing.txt")
	if err != nil {
		t.Fatalf("get() error = %v", err)
	}
	if ok {
		t.Fatal("get() ok = true, want false")
	}
	if got != "" {
		t.Fatalf("get() value = %q, want empty string", got)
	}
}

func TestFileCacheSetOverwriteUpdatesValueAndCurrentSize(t *testing.T) {
	t.Parallel()

	cache := newTestFileCache(t)
	key := "/tmp/demo.txt"

	if err := cache.set(key, "short"); err != nil {
		t.Fatalf("first set() error = %v", err)
	}
	firstSize := cache.currentSize
	if firstSize <= 0 {
		t.Fatalf("currentSize = %d, want > 0", firstSize)
	}

	if err := cache.set(key, "a much longer replacement value"); err != nil {
		t.Fatalf("second set() error = %v", err)
	}

	got, ok, err := cache.get(key)
	if err != nil {
		t.Fatalf("get() error = %v", err)
	}
	if !ok {
		t.Fatal("get() ok = false, want true")
	}
	if got != "a much longer replacement value" {
		t.Fatalf("get() value = %q", got)
	}
	if cache.currentSize <= 0 {
		t.Fatalf("currentSize = %d, want > 0", cache.currentSize)
	}
	if cache.currentSize == firstSize {
		t.Fatalf("currentSize = %d, want changed after overwrite", cache.currentSize)
	}
}

func TestFileCacheDeleteExistingKey(t *testing.T) {
	t.Parallel()

	cache := newTestFileCache(t)
	key := "/tmp/delete-me.txt"
	value := "delete me"

	if err := cache.set(key, value); err != nil {
		t.Fatalf("set() error = %v", err)
	}
	if cache.currentSize == 0 {
		t.Fatal("currentSize = 0, want > 0 after set")
	}

	cachePath := expectedCachePath(cache.directory, key)
	if err := cache.delete(key); err != nil {
		t.Fatalf("delete() error = %v", err)
	}

	if _, err := os.Stat(cachePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("缓存文件仍存在或错误不符: %v", err)
	}
	if cache.currentSize != 0 {
		t.Fatalf("currentSize = %d, want 0", cache.currentSize)
	}
}

func TestFileCacheDeleteMissingKeyIsNoop(t *testing.T) {
	t.Parallel()

	cache := newTestFileCache(t)

	if err := cache.delete("/tmp/not-exist.txt"); err != nil {
		t.Fatalf("delete() error = %v, want nil", err)
	}
	if cache.currentSize != 0 {
		t.Fatalf("currentSize = %d, want 0", cache.currentSize)
	}
}

func TestFileCacheClearRemovesDirectoryAndResetsSize(t *testing.T) {
	t.Parallel()

	cache := newTestFileCache(t)
	if err := cache.set("/tmp/a.txt", "A"); err != nil {
		t.Fatalf("set a error = %v", err)
	}
	if err := cache.set("/tmp/b.txt", "B"); err != nil {
		t.Fatalf("set b error = %v", err)
	}

	if err := cache.clear(); err != nil {
		t.Fatalf("clear() error = %v", err)
	}
	if cache.currentSize != 0 {
		t.Fatalf("currentSize = %d, want 0", cache.currentSize)
	}
	if _, err := os.Stat(cache.directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cache directory still exists or unexpected error: %v", err)
	}
}

func TestFileCacheClearOnMissingDirectory(t *testing.T) {
	t.Parallel()

	cache := newTestFileCache(t)
	if err := cache.clear(); err != nil {
		t.Fatalf("clear() on missing dir error = %v, want nil", err)
	}
	if cache.currentSize != 0 {
		t.Fatalf("currentSize = %d, want 0", cache.currentSize)
	}
}

func TestFileCacheNilReceiver(t *testing.T) {
	t.Parallel()

	var cache *fileCache

	if _, ok, err := cache.get("/tmp/a.txt"); err == nil || ok {
		t.Fatalf("nil cache get() = (%v, %v), want error", ok, err)
	}
	if err := cache.set("/tmp/a.txt", "x"); err == nil {
		t.Fatal("nil cache set() error = nil, want non-nil")
	}
	if err := cache.delete("/tmp/a.txt"); err == nil {
		t.Fatal("nil cache delete() error = nil, want non-nil")
	}
	if err := cache.clear(); err == nil {
		t.Fatal("nil cache clear() error = nil, want non-nil")
	}
}

func TestFileCacheSetSupportsEmptyKeyAndValue(t *testing.T) {
	t.Parallel()

	cache := newTestFileCache(t)
	if err := cache.set("", ""); err != nil {
		t.Fatalf("set(empty, empty) error = %v", err)
	}

	got, ok, err := cache.get("")
	if err != nil {
		t.Fatalf("get(empty) error = %v", err)
	}
	if !ok {
		t.Fatal("get(empty) ok = false, want true")
	}
	if got != "" {
		t.Fatalf("get(empty) value = %q, want empty string", got)
	}
}

func TestFileCacheGetInvalidJSON(t *testing.T) {
	t.Parallel()

	cache := newTestFileCache(t)
	key := "/tmp/bad.json"
	if err := os.MkdirAll(cache.directory, 0o755); err != nil {
		t.Fatalf("创建缓存目录失败: %v", err)
	}

	cachePath := expectedCachePath(cache.directory, key)
	if err := os.WriteFile(cachePath, []byte("{bad json"), 0o644); err != nil {
		t.Fatalf("写入非法 JSON 失败: %v", err)
	}

	_, _, err := cache.get(key)
	if err == nil {
		t.Fatal("get() error = nil, want non-nil for invalid JSON")
	}
}

func TestFileCacheSetFailsWhenDirectoryIsAFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	filePath := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("创建占位文件失败: %v", err)
	}

	cache := &fileCache{
		directory: filePath,
		sizeLimit: 1024,
	}
	if err := cache.set("/tmp/a.txt", "value"); err == nil {
		t.Fatal("set() error = nil, want non-nil when directory path is a file")
	}
}
