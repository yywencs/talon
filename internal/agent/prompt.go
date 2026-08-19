package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	promptassets "github.com/wen/opentalon/prompts"
)

const (
	systemPromptFile       = "system.md"
	defaultInstructionFile = "default-instruction.md"
	promptManifestFile     = "manifest.json"
	incidentIDPlaceholder  = "{{incident_id}}"
	defaultPromptDirectory = "toolops-agent/v4"
)

// PromptSet 是一次 Agent 运行使用的完整 Prompt 资源。
// Version 由维护者声明，Digest 会随 Prompt 内容自动变化。
type PromptSet struct {
	SystemTemplate     string
	DefaultInstruction string
	Version            string
	Digest             string
}

// LoadPromptSet 从 directory 加载可独立发布的 Prompt。directory 为空时使用
// 编译进二进制的默认资源，避免部署遗漏外部文件时改变安全边界。
func LoadPromptSet(directory string) (PromptSet, error) {
	read := func(name string) ([]byte, error) {
		if strings.TrimSpace(directory) == "" {
			return promptassets.Files.ReadFile(defaultPromptDirectory + "/" + name)
		}
		return os.ReadFile(filepath.Join(directory, name))
	}

	system, err := read(systemPromptFile)
	if err != nil {
		return PromptSet{}, fmt.Errorf("read system prompt: %w", err)
	}
	instruction, err := read(defaultInstructionFile)
	if err != nil {
		return PromptSet{}, fmt.Errorf("read default instruction: %w", err)
	}
	manifestPayload, err := read(promptManifestFile)
	if err != nil {
		return PromptSet{}, fmt.Errorf("read prompt manifest: %w", err)
	}
	var manifest struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(manifestPayload, &manifest); err != nil {
		return PromptSet{}, fmt.Errorf("decode prompt manifest: %w", err)
	}

	result := PromptSet{
		SystemTemplate:     strings.TrimSpace(string(system)),
		DefaultInstruction: strings.TrimSpace(string(instruction)),
		Version:            strings.TrimSpace(manifest.ID),
	}
	if result.SystemTemplate == "" || !strings.Contains(result.SystemTemplate, incidentIDPlaceholder) {
		return PromptSet{}, fmt.Errorf("system prompt must contain %s", incidentIDPlaceholder)
	}
	if result.DefaultInstruction == "" {
		return PromptSet{}, fmt.Errorf("default instruction must not be empty")
	}
	if result.Version == "" {
		return PromptSet{}, fmt.Errorf("prompt version must not be empty")
	}
	digest := sha256.Sum256([]byte(result.SystemTemplate + "\x00" + result.DefaultInstruction))
	result.Digest = hex.EncodeToString(digest[:])
	return result, nil
}

func (p PromptSet) systemPrompt(incidentID, additional string) string {
	prompt := strings.ReplaceAll(p.SystemTemplate, incidentIDPlaceholder, strconv.Quote(incidentID))
	additional = strings.TrimSpace(additional)
	if additional == "" {
		return prompt
	}
	return prompt + "\n\n当前部署的补充调查说明：\n" + additional
}
