package approval

import (
	"fmt"
	"path/filepath"

	"github.com/wen/opentalon/internal/config"
)

const databaseFileName = "talon.db"

// DefaultDatabasePath 返回 Talon 控制面 SQLite 的默认位置。
// OPENTALON_CONFIG_DIR 存在时使用该目录，否则使用 ~/.opentalon/talon.db。
func DefaultDatabasePath() (string, error) {
	root, err := config.FindRoot()
	if err != nil {
		return "", fmt.Errorf("find approval database root: %w", err)
	}
	return filepath.Join(root, databaseFileName), nil
}

// OpenDefaultSQLiteStore 打开 Talon 默认的持久化审批存储。
func OpenDefaultSQLiteStore() (*SQLiteStore, error) {
	path, err := DefaultDatabasePath()
	if err != nil {
		return nil, err
	}
	return NewSQLiteStore(path)
}
