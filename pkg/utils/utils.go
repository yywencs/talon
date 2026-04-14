package utils

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrWorkspaceNotFound = errors.New("无法定位 Workspace 根目录（未找到 go.mod 文件夹）")

func FindWorkspaceRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("获取当前工作目录失败: %w", err)
	}

	// 向上遍历，最多找 10 层，防止死循环
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		// 往上一层
		parent := filepath.Dir(dir)
		if parent == dir {
			break // 到达系统根目录
		}
		dir = parent
	}

	return "", ErrWorkspaceNotFound
}
