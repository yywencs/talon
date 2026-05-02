package tool

import (
	"path/filepath"
	"strings"
)

// PathMapper 定义宿主机工作区根目录与 runtime 工作区根目录之间的最小映射关系。
type PathMapper struct {
	HostRoot    string
	RuntimeRoot string
}

// NewPathMapper 创建路径映射器。
func NewPathMapper(hostRoot, runtimeRoot string) PathMapper {
	return PathMapper{
		HostRoot:    cleanRoot(hostRoot),
		RuntimeRoot: cleanRoot(runtimeRoot),
	}
}

// HostToRuntime 将宿主机绝对路径映射为 runtime 绝对路径。
func (m PathMapper) HostToRuntime(hostPath string) (string, bool) {
	return mapPath(hostPath, cleanRoot(m.HostRoot), cleanRoot(m.RuntimeRoot))
}

// RuntimeToHost 将 runtime 绝对路径映射为宿主机绝对路径。
func (m PathMapper) RuntimeToHost(runtimePath string) (string, bool) {
	return mapPath(runtimePath, cleanRoot(m.RuntimeRoot), cleanRoot(m.HostRoot))
}

// IsHostPath 判断给定路径是否位于宿主机工作区根目录下。
func (m PathMapper) IsHostPath(hostPath string) bool {
	_, ok := relativeToRoot(cleanRoot(m.HostRoot), hostPath)
	return ok
}

// IsRuntimePath 判断给定路径是否位于 runtime 工作区根目录下。
func (m PathMapper) IsRuntimePath(runtimePath string) bool {
	_, ok := relativeToRoot(cleanRoot(m.RuntimeRoot), runtimePath)
	return ok
}

func mapPath(inputPath, fromRoot, toRoot string) (string, bool) {
	relPath, ok := relativeToRoot(fromRoot, inputPath)
	if !ok || toRoot == "" {
		return "", false
	}
	if relPath == "" {
		return toRoot, true
	}
	return filepath.Join(toRoot, relPath), true
}

func relativeToRoot(root, inputPath string) (string, bool) {
	if root == "" || inputPath == "" {
		return "", false
	}
	cleanInput := filepath.Clean(inputPath)
	relPath, err := filepath.Rel(root, cleanInput)
	if err != nil {
		return "", false
	}
	if relPath == "." {
		return "", true
	}
	parentPrefix := ".." + string(filepath.Separator)
	if relPath == ".." || strings.HasPrefix(relPath, parentPrefix) {
		return "", false
	}
	return relPath, true
}

func cleanRoot(root string) string {
	if root == "" {
		return ""
	}
	return filepath.Clean(root)
}
