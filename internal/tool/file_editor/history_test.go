package file_editor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func newTestHistoryManager(t *testing.T, maxHistoryPerFile int) *fileHistoryManager {
	t.Helper()

	cache := newTestFileCache(t)
	return newFileHistoryManager(maxHistoryPerFile, cache)
}

func TestFileHistoryManagerAddAndPopLIFO(t *testing.T) {
	t.Parallel()

	manager := newTestHistoryManager(t, 10)
	filePath := "/tmp/demo.txt"

	if err := manager.add(filePath, "v1"); err != nil {
		t.Fatalf("add v1 error = %v", err)
	}
	if err := manager.add(filePath, "v2"); err != nil {
		t.Fatalf("add v2 error = %v", err)
	}

	metadata, err := manager.loadMetadata(filePath)
	if err != nil {
		t.Fatalf("loadMetadata() error = %v", err)
	}
	if metadata.VersionCount != 2 {
		t.Fatalf("VersionCount = %d, want 2", metadata.VersionCount)
	}

	got, ok, err := manager.pop(filePath)
	if err != nil {
		t.Fatalf("first pop() error = %v", err)
	}
	if !ok || got != "v2" {
		t.Fatalf("first pop() = (%q, %v), want (v2, true)", got, ok)
	}

	got, ok, err = manager.pop(filePath)
	if err != nil {
		t.Fatalf("second pop() error = %v", err)
	}
	if !ok || got != "v1" {
		t.Fatalf("second pop() = (%q, %v), want (v1, true)", got, ok)
	}

	got, ok, err = manager.pop(filePath)
	if err != nil {
		t.Fatalf("third pop() error = %v", err)
	}
	if ok || got != "" {
		t.Fatalf("third pop() = (%q, %v), want (\"\", false)", got, ok)
	}
}

func TestFileHistoryManagerAddTrimsOldestVersion(t *testing.T) {
	t.Parallel()

	manager := newTestHistoryManager(t, 2)
	filePath := "/tmp/trim.txt"

	if err := manager.add(filePath, "v1"); err != nil {
		t.Fatalf("add v1 error = %v", err)
	}
	if err := manager.add(filePath, "v2"); err != nil {
		t.Fatalf("add v2 error = %v", err)
	}
	if err := manager.add(filePath, "v3"); err != nil {
		t.Fatalf("add v3 error = %v", err)
	}

	metadata, err := manager.loadMetadata(filePath)
	if err != nil {
		t.Fatalf("loadMetadata() error = %v", err)
	}
	if metadata.VersionCount != 2 {
		t.Fatalf("VersionCount = %d, want 2", metadata.VersionCount)
	}
	if len(metadata.Versions) != 2 {
		t.Fatalf("len(Versions) = %d, want 2", len(metadata.Versions))
	}

	got, ok, err := manager.pop(filePath)
	if err != nil {
		t.Fatalf("first pop() error = %v", err)
	}
	if !ok || got != "v3" {
		t.Fatalf("first pop() = (%q, %v), want (v3, true)", got, ok)
	}

	got, ok, err = manager.pop(filePath)
	if err != nil {
		t.Fatalf("second pop() error = %v", err)
	}
	if !ok || got != "v2" {
		t.Fatalf("second pop() = (%q, %v), want (v2, true)", got, ok)
	}

	got, ok, err = manager.pop(filePath)
	if err != nil {
		t.Fatalf("third pop() error = %v", err)
	}
	if ok || got != "" {
		t.Fatalf("third pop() = (%q, %v), want empty + false", got, ok)
	}
}

func TestFileHistoryManagerAddFailsWhenVersionWriteFails(t *testing.T) {
	t.Parallel()

	cache, err := newFileCache(filepath.Join(t.TempDir(), "cache"), 10)
	if err != nil {
		t.Fatalf("newFileCache() error = %v", err)
	}
	manager := newFileHistoryManager(10, cache)

	err = manager.add("/tmp/huge.txt", "this payload is far beyond the configured limit")
	if err == nil {
		t.Fatal("add() error = nil, want non-nil")
	}
}

func TestFileHistoryManagerAddFailsWhenMetadataIsInvalidJSON(t *testing.T) {
	t.Parallel()

	manager := newTestHistoryManager(t, 10)
	filePath := "/tmp/bad-metadata.txt"

	if err := manager.cache.set(manager.metadataCacheKey(filePath), "{bad json"); err != nil {
		t.Fatalf("seed invalid metadata error = %v", err)
	}

	if err := manager.add(filePath, "content"); err == nil {
		t.Fatal("add() error = nil, want non-nil for invalid metadata")
	}
}

func TestFileHistoryManagerPopEmptyHistory(t *testing.T) {
	t.Parallel()

	manager := newTestHistoryManager(t, 10)
	got, ok, err := manager.pop("/tmp/empty.txt")
	if err != nil {
		t.Fatalf("pop() error = %v", err)
	}
	if ok || got != "" {
		t.Fatalf("pop() = (%q, %v), want empty + false", got, ok)
	}
}

func TestFileHistoryManagerPopMissingVersionContentRepairsMetadata(t *testing.T) {
	t.Parallel()

	manager := newTestHistoryManager(t, 10)
	filePath := "/tmp/missing-version.txt"
	if err := manager.add(filePath, "v1"); err != nil {
		t.Fatalf("add() error = %v", err)
	}

	metadata, err := manager.loadMetadata(filePath)
	if err != nil {
		t.Fatalf("loadMetadata() error = %v", err)
	}
	if len(metadata.Versions) != 1 {
		t.Fatalf("len(Versions) = %d, want 1", len(metadata.Versions))
	}
	if deleteErr := manager.cache.delete(metadata.Versions[0].CacheKey); deleteErr != nil {
		t.Fatalf("delete version cache error = %v", deleteErr)
	}

	got, ok, err := manager.pop(filePath)
	if err != nil {
		t.Fatalf("pop() error = %v", err)
	}
	if ok || got != "" {
		t.Fatalf("pop() = (%q, %v), want empty + false", got, ok)
	}

	raw, found, getErr := manager.cache.get(manager.metadataCacheKey(filePath))
	if getErr != nil {
		t.Fatalf("get metadata cache error = %v", getErr)
	}
	if found || raw != "" {
		t.Fatalf("metadata should be deleted after repairing missing version, got found=%v raw=%q", found, raw)
	}
}

func TestFileHistoryManagerPopFailsWhenMetadataInvalidJSON(t *testing.T) {
	t.Parallel()

	manager := newTestHistoryManager(t, 10)
	filePath := "/tmp/pop-invalid.txt"
	if err := manager.cache.set(manager.metadataCacheKey(filePath), "{bad json"); err != nil {
		t.Fatalf("seed invalid metadata error = %v", err)
	}

	if _, _, err := manager.pop(filePath); err == nil {
		t.Fatal("pop() error = nil, want non-nil for invalid metadata")
	}
}

func TestFileHistoryManagerDeleteRemovesVersionsAndMetadata(t *testing.T) {
	t.Parallel()

	manager := newTestHistoryManager(t, 10)
	filePath := "/tmp/delete.txt"
	if err := manager.add(filePath, "v1"); err != nil {
		t.Fatalf("add v1 error = %v", err)
	}
	if err := manager.add(filePath, "v2"); err != nil {
		t.Fatalf("add v2 error = %v", err)
	}

	metadata, err := manager.loadMetadata(filePath)
	if err != nil {
		t.Fatalf("loadMetadata() error = %v", err)
	}

	if err := manager.delete(filePath); err != nil {
		t.Fatalf("delete() error = %v", err)
	}

	raw, found, getErr := manager.cache.get(manager.metadataCacheKey(filePath))
	if getErr != nil {
		t.Fatalf("get metadata after delete error = %v", getErr)
	}
	if found || raw != "" {
		t.Fatalf("metadata should be deleted, found=%v raw=%q", found, raw)
	}

	for _, version := range metadata.Versions {
		raw, found, getErr := manager.cache.get(version.CacheKey)
		if getErr != nil {
			t.Fatalf("get version cache error = %v", getErr)
		}
		if found || raw != "" {
			t.Fatalf("version cache should be deleted, found=%v raw=%q", found, raw)
		}
	}
}

func TestFileHistoryManagerDeleteMissingHistoryIsNoop(t *testing.T) {
	t.Parallel()

	manager := newTestHistoryManager(t, 10)
	if err := manager.delete("/tmp/missing-delete.txt"); err != nil {
		t.Fatalf("delete() error = %v, want nil", err)
	}
}

func TestFileHistoryManagerClearRemovesAllHistory(t *testing.T) {
	t.Parallel()

	manager := newTestHistoryManager(t, 10)
	if err := manager.add("/tmp/a.txt", "A1"); err != nil {
		t.Fatalf("add a error = %v", err)
	}
	if err := manager.add("/tmp/b.txt", "B1"); err != nil {
		t.Fatalf("add b error = %v", err)
	}

	if err := manager.clear(); err != nil {
		t.Fatalf("clear() error = %v", err)
	}
	if manager.cache.currentSize != 0 {
		t.Fatalf("currentSize = %d, want 0", manager.cache.currentSize)
	}
	entries, readErr := os.ReadDir(manager.cache.directory)
	if readErr != nil {
		t.Fatalf("ReadDir() error = %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("cache directory should be empty after clear, got %d entries", len(entries))
	}
}

func TestFileHistoryManagerLoadMetadataDefaults(t *testing.T) {
	t.Parallel()

	manager := newTestHistoryManager(t, 10)
	filePath := "/tmp/defaults.txt"

	rawMetadata, err := json.Marshal(fileHistoryMetadata{})
	if err != nil {
		t.Fatalf("marshal metadata error = %v", err)
	}
	if setErr := manager.cache.set(manager.metadataCacheKey(filePath), string(rawMetadata)); setErr != nil {
		t.Fatalf("seed metadata error = %v", setErr)
	}

	metadata, err := manager.loadMetadata(filePath)
	if err != nil {
		t.Fatalf("loadMetadata() error = %v", err)
	}
	if metadata.FilePath != filePath {
		t.Fatalf("FilePath = %q, want %q", metadata.FilePath, filePath)
	}
	if metadata.Versions == nil {
		t.Fatal("Versions = nil, want empty slice")
	}
	if metadata.VersionCount != 0 {
		t.Fatalf("VersionCount = %d, want 0", metadata.VersionCount)
	}
}

func TestFileHistoryManagerNilAndNilCache(t *testing.T) {
	t.Parallel()

	var nilManager *fileHistoryManager
	if err := nilManager.add("/tmp/a.txt", "x"); err == nil {
		t.Fatal("nil manager add() error = nil, want non-nil")
	}
	if _, _, err := nilManager.pop("/tmp/a.txt"); err == nil {
		t.Fatal("nil manager pop() error = nil, want non-nil")
	}
	if err := nilManager.delete("/tmp/a.txt"); err == nil {
		t.Fatal("nil manager delete() error = nil, want non-nil")
	}
	if err := nilManager.clear(); err == nil {
		t.Fatal("nil manager clear() error = nil, want non-nil")
	}

	manager := newFileHistoryManager(10, nil)
	if err := manager.add("/tmp/a.txt", "x"); err == nil {
		t.Fatal("nil cache add() error = nil, want non-nil")
	}
	if _, _, err := manager.pop("/tmp/a.txt"); err == nil {
		t.Fatal("nil cache pop() error = nil, want non-nil")
	}
	if err := manager.delete("/tmp/a.txt"); err == nil {
		t.Fatal("nil cache delete() error = nil, want non-nil")
	}
	if err := manager.clear(); err == nil {
		t.Fatal("nil cache clear() error = nil, want non-nil")
	}
	if _, err := manager.loadMetadata("/tmp/a.txt"); err == nil {
		t.Fatal("nil cache loadMetadata() error = nil, want non-nil")
	}
}
