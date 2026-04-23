package file_editor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
		return "", false, err
	}

	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return "", false, err
	}
	return entry.Value, true, nil
}

func (c *fileCache) set(key, value string) error {
	if c == nil {
		return errors.New("fileCache is nil")
	}

	if err := os.MkdirAll(c.directory, 0o755); err != nil {
		return err
	}

	path := c.cacheFilePath(key)
	oldSize := int64(0)
	if info, err := os.Stat(path); err == nil {
		oldSize = info.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	entry := cacheEntry{
		Key:   key,
		Value: value,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}

	c.currentSize = c.currentSize - oldSize + int64(len(data))
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
		return err
	}

	if err := os.Remove(path); err != nil {
		return err
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

	if err := os.RemoveAll(c.directory); err != nil {
		return err
	}
	c.currentSize = 0
	return nil
}

func (c *fileCache) cacheFilePath(key string) string {
	sum := sha256.Sum256([]byte(key))
	filename := hex.EncodeToString(sum[:]) + ".json"
	return filepath.Join(c.directory, filename)
}
