package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const configDirEnv = "OPENTALON_CONFIG_DIR"

// FindRoot 返回配置目录，允许通过 OPENTALON_CONFIG_DIR 覆盖默认位置。
func FindRoot() (string, error) {
	if configDir := strings.TrimSpace(os.Getenv(configDirEnv)); configDir != "" {
		return configDir, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取用户 Home 目录失败: %w", err)
	}

	return filepath.Join(home, ".opentalon"), nil
}
