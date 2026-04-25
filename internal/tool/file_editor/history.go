package file_editor

import (
	"encoding/json"
	"fmt"
	"time"
)

type fileHistoryManager struct {
	maxHistoryPerFile int
	cache             *fileCache
}

type fileHistoryMetadata struct {
	FilePath     string               `json:"file_path"`
	VersionCount int                  `json:"version_count"`
	Versions     []fileHistoryVersion `json:"versions"`
}

type fileHistoryVersion struct {
	ID        string `json:"id"`
	CacheKey  string `json:"cache_key"`
	CreatedAt string `json:"created_at"`
}

func newFileHistoryManager(maxHistoryPerFile int, cache *fileCache) *fileHistoryManager {
	return &fileHistoryManager{
		maxHistoryPerFile: maxHistoryPerFile,
		cache:             cache,
	}
}

func (m *fileHistoryManager) add(filePath, content string) error {
	if m == nil {
		return fmt.Errorf("fileHistoryManager is nil")
	}
	if m.cache == nil {
		return fmt.Errorf("fileHistoryManager cache is nil")
	}

	metadata, err := m.loadMetadata(filePath)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	versionID := now.UnixNano()
	version := fileHistoryVersion{
		ID:        fmt.Sprintf("%d", versionID),
		CacheKey:  m.versionCacheKey(filePath, versionID),
		CreatedAt: now.Format(time.RFC3339Nano),
	}
	if err := m.cache.set(version.CacheKey, content); err != nil {
		return err
	}

	metadata.FilePath = filePath
	metadata.Versions = append(metadata.Versions, version)
	metadata.VersionCount = len(metadata.Versions)

	if m.maxHistoryPerFile > 0 {
		for len(metadata.Versions) > m.maxHistoryPerFile {
			oldest := metadata.Versions[0]
			if err := m.cache.delete(oldest.CacheKey); err != nil {
				return err
			}
			metadata.Versions = metadata.Versions[1:]
			metadata.VersionCount = len(metadata.Versions)
		}
	}

	return m.saveMetadata(metadata)
}

func (m *fileHistoryManager) pop(filePath string) (string, bool, error) {
	if m == nil {
		return "", false, fmt.Errorf("fileHistoryManager is nil")
	}
	if m.cache == nil {
		return "", false, fmt.Errorf("fileHistoryManager cache is nil")
	}

	metadata, err := m.loadMetadata(filePath)
	if err != nil {
		return "", false, err
	}
	if len(metadata.Versions) == 0 {
		return "", false, nil
	}

	lastIndex := len(metadata.Versions) - 1
	latest := metadata.Versions[lastIndex]
	content, found, err := m.cache.get(latest.CacheKey)
	if err != nil {
		return "", false, err
	}
	if !found {
		metadata.Versions = metadata.Versions[:lastIndex]
		metadata.VersionCount = len(metadata.Versions)
		if err := m.saveOrDeleteMetadata(filePath, metadata); err != nil {
			return "", false, err
		}
		return "", false, nil
	}

	if err := m.cache.delete(latest.CacheKey); err != nil {
		return "", false, err
	}
	metadata.Versions = metadata.Versions[:lastIndex]
	metadata.VersionCount = len(metadata.Versions)
	if err := m.saveOrDeleteMetadata(filePath, metadata); err != nil {
		return "", false, err
	}
	return content, true, nil
}

func (m *fileHistoryManager) delete(filePath string) error {
	if m == nil {
		return fmt.Errorf("fileHistoryManager is nil")
	}
	if m.cache == nil {
		return fmt.Errorf("fileHistoryManager cache is nil")
	}

	metadata, err := m.loadMetadata(filePath)
	if err != nil {
		return err
	}
	for _, version := range metadata.Versions {
		if err := m.cache.delete(version.CacheKey); err != nil {
			return err
		}
	}
	return m.cache.delete(m.metadataCacheKey(filePath))
}

func (m *fileHistoryManager) clear() error {
	if m == nil {
		return fmt.Errorf("fileHistoryManager is nil")
	}
	if m.cache == nil {
		return fmt.Errorf("fileHistoryManager cache is nil")
	}
	return m.cache.clear()
}

func (m *fileHistoryManager) loadMetadata(filePath string) (*fileHistoryMetadata, error) {
	if m.cache == nil {
		return nil, fmt.Errorf("fileHistoryManager cache is nil")
	}

	raw, found, err := m.cache.get(m.metadataCacheKey(filePath))
	if err != nil {
		return nil, err
	}
	if !found {
		return &fileHistoryMetadata{
			FilePath: filePath,
			Versions: make([]fileHistoryVersion, 0),
		}, nil
	}

	var metadata fileHistoryMetadata
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return nil, err
	}
	if metadata.Versions == nil {
		metadata.Versions = make([]fileHistoryVersion, 0)
	}
	metadata.VersionCount = len(metadata.Versions)
	if metadata.FilePath == "" {
		metadata.FilePath = filePath
	}
	return &metadata, nil
}

func (m *fileHistoryManager) saveMetadata(metadata *fileHistoryMetadata) error {
	if m.cache == nil {
		return fmt.Errorf("fileHistoryManager cache is nil")
	}
	if metadata == nil {
		return fmt.Errorf("metadata is nil")
	}

	metadata.VersionCount = len(metadata.Versions)
	data, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	return m.cache.set(m.metadataCacheKey(metadata.FilePath), string(data))
}

func (m *fileHistoryManager) saveOrDeleteMetadata(filePath string, metadata *fileHistoryMetadata) error {
	if metadata == nil || len(metadata.Versions) == 0 {
		return m.cache.delete(m.metadataCacheKey(filePath))
	}
	return m.saveMetadata(metadata)
}

func (m *fileHistoryManager) metadataCacheKey(filePath string) string {
	return "history:metadata:" + filePath
}

func (m *fileHistoryManager) versionCacheKey(filePath string, versionID int64) string {
	return fmt.Sprintf("history:version:%s:%d", filePath, versionID)
}
