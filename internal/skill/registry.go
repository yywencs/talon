// Package skill 负责发现和加载 Talon 的可安装 Agent Skill。
package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

const (
	DefaultDirectory = "skills"
	SkillFileName    = "SKILL.md"
	PolicyFileName   = "talon.yaml"
	PolicySchemaV1   = "talon-skill/v1"
	MaxSkillBytes    = 128 << 10
)

var (
	skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	toolNamePattern  = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// Definition 是加载后不可变的 Skill 快照。Instructions 只包含 frontmatter 后的正文。
// Digest 覆盖 SKILL.md 和 talon.yaml，便于 Run Artifact 标识实际使用的 Skill 内容。
type Definition struct {
	Name         string
	Description  string
	Instructions string
	AllowedTools []string
	Digest       string
	Directory    string
}

// CatalogEntry 是可以常驻模型上下文的轻量目录项，不包含 Skill 正文和工具策略。
type CatalogEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Registry 保存一次目录扫描得到的 Skill 快照。
type Registry struct {
	byName map[string]Definition
	names  []string
}

type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type policy struct {
	SchemaVersion string   `yaml:"schema_version"`
	AllowedTools  []string `yaml:"allowed_tools"`
}

// LoadDirectory 扫描 root 的直接子目录。每个 Skill 必须包含 SKILL.md；talon.yaml
// 是可选的 Talon 工具策略，缺失时该 Skill 不获得任何专用工具。
func LoadDirectory(root string) (*Registry, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("skill directory is required")
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat skill directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("skill path %q is not a directory", root)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read skill directory: %w", err)
	}

	registry := &Registry{byName: make(map[string]Definition)}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") || !entry.IsDir() {
			continue
		}
		definition, loadErr := loadDefinition(root, entry.Name())
		if loadErr != nil {
			return nil, fmt.Errorf("load skill %q: %w", entry.Name(), loadErr)
		}
		if _, exists := registry.byName[definition.Name]; exists {
			return nil, fmt.Errorf("duplicate skill name %q", definition.Name)
		}
		registry.byName[definition.Name] = definition
		registry.names = append(registry.names, definition.Name)
	}
	sort.Strings(registry.names)
	return registry, nil
}

// Len 返回 Registry 中 Skill 的数量。
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	return len(r.names)
}

// Catalog 返回按名称排序的轻量 Skill 目录。
func (r *Registry) Catalog() []CatalogEntry {
	if r == nil {
		return nil
	}
	result := make([]CatalogEntry, 0, len(r.names))
	for _, name := range r.names {
		definition := r.byName[name]
		result = append(result, CatalogEntry{Name: definition.Name, Description: definition.Description})
	}
	return result
}

// Get 返回指定 Skill 的副本，调用方修改 AllowedTools 不会影响 Registry。
func (r *Registry) Get(name string) (Definition, bool) {
	if r == nil {
		return Definition{}, false
	}
	definition, exists := r.byName[strings.TrimSpace(name)]
	if !exists {
		return Definition{}, false
	}
	definition.AllowedTools = append([]string(nil), definition.AllowedTools...)
	return definition, true
}

func loadDefinition(root, directoryName string) (Definition, error) {
	if !skillNamePattern.MatchString(directoryName) || len(directoryName) > 64 {
		return Definition{}, fmt.Errorf("directory name must be lowercase hyphen-case with at most 64 characters")
	}
	directory := filepath.Join(root, directoryName)
	skillPath := filepath.Join(directory, SkillFileName)
	skillData, err := readRegularFile(skillPath, true)
	if err != nil {
		return Definition{}, err
	}
	metadata, instructions, err := parseSkill(skillData)
	if err != nil {
		return Definition{}, err
	}
	if metadata.Name != directoryName {
		return Definition{}, fmt.Errorf("frontmatter name %q does not match directory %q", metadata.Name, directoryName)
	}

	policyPath := filepath.Join(directory, PolicyFileName)
	policyData, err := readRegularFile(policyPath, false)
	if err != nil {
		return Definition{}, err
	}
	loadedPolicy, err := parsePolicy(policyData)
	if err != nil {
		return Definition{}, err
	}

	digestInput := make([]byte, 0, len(skillData)+len(policyData)+1)
	digestInput = append(digestInput, skillData...)
	digestInput = append(digestInput, 0)
	digestInput = append(digestInput, policyData...)
	digest := sha256.Sum256(digestInput)
	return Definition{
		Name: metadata.Name, Description: metadata.Description, Instructions: instructions,
		AllowedTools: loadedPolicy.AllowedTools,
		Digest:       "sha256:" + hex.EncodeToString(digest[:]), Directory: directory,
	}, nil
}

func readRegularFile(path string, required bool) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if !required && os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat %s: %w", filepath.Base(path), err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", filepath.Base(path))
	}
	if info.Size() > MaxSkillBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", filepath.Base(path), MaxSkillBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	return data, nil
}

func parseSkill(data []byte) (frontmatter, string, error) {
	text := strings.ReplaceAll(strings.TrimPrefix(string(data), "\ufeff"), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return frontmatter{}, "", fmt.Errorf("%s must start with YAML frontmatter", SkillFileName)
	}
	remainder := strings.TrimPrefix(text, "---\n")
	boundary := strings.Index(remainder, "\n---\n")
	if boundary < 0 {
		return frontmatter{}, "", fmt.Errorf("%s frontmatter is not terminated", SkillFileName)
	}
	var metadata frontmatter
	if err := yaml.Unmarshal([]byte(remainder[:boundary]), &metadata); err != nil {
		return frontmatter{}, "", fmt.Errorf("parse %s frontmatter: %w", SkillFileName, err)
	}
	metadata.Name = strings.TrimSpace(metadata.Name)
	metadata.Description = strings.TrimSpace(metadata.Description)
	if !skillNamePattern.MatchString(metadata.Name) || len(metadata.Name) > 64 {
		return frontmatter{}, "", fmt.Errorf("frontmatter name must be lowercase hyphen-case with at most 64 characters")
	}
	if metadata.Description == "" {
		return frontmatter{}, "", fmt.Errorf("frontmatter description is required")
	}
	if len([]rune(metadata.Description)) > 1024 {
		return frontmatter{}, "", fmt.Errorf("frontmatter description exceeds 1024 characters")
	}
	instructions := strings.TrimSpace(remainder[boundary+len("\n---\n"):])
	if instructions == "" {
		return frontmatter{}, "", fmt.Errorf("%s instructions are required", SkillFileName)
	}
	return metadata, instructions, nil
}

func parsePolicy(data []byte) (policy, error) {
	if len(data) == 0 {
		return policy{}, nil
	}
	var value policy
	if err := yaml.Unmarshal(data, &value); err != nil {
		return policy{}, fmt.Errorf("parse %s: %w", PolicyFileName, err)
	}
	if value.SchemaVersion != PolicySchemaV1 {
		return policy{}, fmt.Errorf("%s schema_version must be %q", PolicyFileName, PolicySchemaV1)
	}
	seen := make(map[string]struct{}, len(value.AllowedTools))
	allowedTools := make([]string, 0, len(value.AllowedTools))
	for index, name := range value.AllowedTools {
		name = strings.TrimSpace(name)
		if !toolNamePattern.MatchString(name) {
			return policy{}, fmt.Errorf("%s allowed_tools[%d] %q is invalid", PolicyFileName, index, name)
		}
		if _, duplicate := seen[name]; duplicate {
			return policy{}, fmt.Errorf("%s contains duplicate allowed tool %q", PolicyFileName, name)
		}
		seen[name] = struct{}{}
		allowedTools = append(allowedTools, name)
	}
	value.AllowedTools = allowedTools
	return value, nil
}
