package file_editor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestFileCache(t *testing.T) *fileCache {
	t.Helper()
	cache, err := newFileCache(filepath.Join(t.TempDir(), "cache"), 1024)
	if err != nil {
		t.Fatalf("newFileCache() error = %v", err)
	}
	return cache
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

func TestNewFileCacheCreatesDirectoryAndLoadsCurrentSize(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建缓存目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.json"), []byte("12345"), 0o644); err != nil {
		t.Fatalf("写入缓存文件 a 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.json"), []byte("1234567"), 0o644); err != nil {
		t.Fatalf("写入缓存文件 b 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("123456789"), 0o644); err != nil {
		t.Fatalf("写入非 json 文件失败: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatalf("创建嵌套目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "nested.json"), []byte("999999"), 0o644); err != nil {
		t.Fatalf("写入嵌套缓存文件失败: %v", err)
	}

	cache, err := newFileCache(dir, 2048)
	if err != nil {
		t.Fatalf("newFileCache() error = %v", err)
	}
	if cache.directory != dir {
		t.Fatalf("directory = %q, want %q", cache.directory, dir)
	}
	if cache.sizeLimit != 2048 {
		t.Fatalf("sizeLimit = %d, want 2048", cache.sizeLimit)
	}
	if cache.currentSize != 12 {
		t.Fatalf("currentSize = %d, want 12 (only flat .json files counted)", cache.currentSize)
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

func TestFileCacheGetRefreshesUsageTime(t *testing.T) {
	t.Parallel()

	cache := newTestFileCache(t)
	key := "/tmp/refresh.txt"
	if err := cache.set(key, "value"); err != nil {
		t.Fatalf("set() error = %v", err)
	}

	cachePath := expectedCachePath(cache.directory, key)
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(cachePath, oldTime, oldTime); err != nil {
		t.Fatalf("设置旧时间失败: %v", err)
	}

	beforeInfo, err := os.Stat(cachePath)
	if err != nil {
		t.Fatalf("stat before get failed: %v", err)
	}

	got, ok, err := cache.get(key)
	if err != nil {
		t.Fatalf("get() error = %v", err)
	}
	if !ok || got != "value" {
		t.Fatalf("get() = (%q, %v), want (value, true)", got, ok)
	}

	afterInfo, err := os.Stat(cachePath)
	if err != nil {
		t.Fatalf("stat after get failed: %v", err)
	}
	if !afterInfo.ModTime().After(beforeInfo.ModTime()) {
		t.Fatalf("ModTime after get = %v, want after %v", afterInfo.ModTime(), beforeInfo.ModTime())
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
	info, err := os.Stat(cache.directory)
	if err != nil {
		t.Fatalf("cache directory should still exist: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("cache path should remain a directory")
	}
	entries, err := os.ReadDir(cache.directory)
	if err != nil {
		t.Fatalf("读取缓存目录失败: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("cache directory should be empty after clear, got %d entries", len(entries))
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

func TestFileCacheSetEvictsLeastRecentlyUsedWhenOverSizeLimit(t *testing.T) {
	t.Parallel()

	sizeB := int64(len(mustMarshalCacheEntry(t, "/tmp/b.txt", "bbbbbbbbbb")))
	sizeC := int64(len(mustMarshalCacheEntry(t, "/tmp/c.txt", "cccccccccccccccccccc")))

	cache, err := newFileCache(filepath.Join(t.TempDir(), "cache"), sizeB+sizeC)
	if err != nil {
		t.Fatalf("newFileCache() error = %v", err)
	}

	if err := cache.set("/tmp/a.txt", "aaaaaaaaaa"); err != nil {
		t.Fatalf("set a error = %v", err)
	}
	aPath := expectedCachePath(cache.directory, "/tmp/a.txt")
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(aPath, oldTime, oldTime); err != nil {
		t.Fatalf("设置 a 的时间失败: %v", err)
	}

	if err := cache.set("/tmp/b.txt", "bbbbbbbbbb"); err != nil {
		t.Fatalf("set b error = %v", err)
	}
	bPath := expectedCachePath(cache.directory, "/tmp/b.txt")
	midTime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(bPath, midTime, midTime); err != nil {
		t.Fatalf("设置 b 的时间失败: %v", err)
	}

	if err := cache.set("/tmp/c.txt", "cccccccccccccccccccc"); err != nil {
		t.Fatalf("set c error = %v", err)
	}

	if _, err := os.Stat(aPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("最旧缓存文件 a 应被淘汰, stat err = %v", err)
	}
	if _, err := os.Stat(bPath); err != nil {
		t.Fatalf("较新的缓存文件 b 不应被淘汰: %v", err)
	}
	cPath := expectedCachePath(cache.directory, "/tmp/c.txt")
	if _, err := os.Stat(cPath); err != nil {
		t.Fatalf("新写入缓存文件 c 不存在: %v", err)
	}
	if cache.currentSize > cache.sizeLimit {
		t.Fatalf("currentSize = %d, sizeLimit = %d, want currentSize <= sizeLimit", cache.currentSize, cache.sizeLimit)
	}
	if cache.currentSize != sizeB+sizeC {
		t.Fatalf("currentSize = %d, want %d", cache.currentSize, sizeB+sizeC)
	}
}

func TestFileCacheSetReturnsErrorWhenSingleEntryStillExceedsLimit(t *testing.T) {
	t.Parallel()

	cache, err := newFileCache(filepath.Join(t.TempDir(), "cache"), 10)
	if err != nil {
		t.Fatalf("newFileCache() error = %v", err)
	}

	err = cache.set("/tmp/huge.txt", "this payload is far beyond the configured limit")
	if err == nil {
		t.Fatal("set() error = nil, want non-nil when single entry exceeds size limit")
	}

	cachePath := expectedCachePath(cache.directory, "/tmp/huge.txt")
	if _, statErr := os.Stat(cachePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("超限文件不应被写入, stat err = %v", statErr)
	}
}

func mustMarshalCacheEntry(t *testing.T, key, value string) []byte {
	t.Helper()

	data, err := json.Marshal(cacheEntry{
		Key:   key,
		Value: value,
	})
	if err != nil {
		t.Fatalf("marshal cache entry failed: %v", err)
	}
	return data
}
