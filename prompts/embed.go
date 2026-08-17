// Package prompts 提供随 Talon 二进制发布的版本化 Prompt 资源。
package prompts

import "embed"

// Files 包含所有已发布的 ToolOps Agent Prompt 版本。
//
//go:embed toolops-agent/*/*.md toolops-agent/*/manifest.json
var Files embed.FS
