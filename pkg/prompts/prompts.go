package prompts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type Prompter interface {
	Base() string
	Mode() string
}

type Registry struct {
	fsDir string
}

func NewRegistry(fsDir string) *Registry {
	return &Registry{fsDir: fsDir}
}

func (r *Registry) Get(name string) Prompter {
	if r != nil && r.fsDir != "" {
		jsonPath := filepath.Join(r.fsDir, name+".json")
		if data, err := os.ReadFile(jsonPath); err == nil {
			var p PromptFile
			if err := json.Unmarshal(data, &p); err == nil {
				return &filePrompter{
					base: p.Base,
					mode: p.Mode,
				}
			}
		}
		basePath := filepath.Join(r.fsDir, name+".txt")
		if data, err := os.ReadFile(basePath); err == nil {
			mode := ""
			if m, err := os.ReadFile(filepath.Join(r.fsDir, name+"_mode.txt")); err == nil {
				mode = strings.TrimSpace(string(m))
			}
			return &filePrompter{
				base: strings.TrimSpace(string(data)),
				mode: mode,
			}
		}
	}
	return &builtinPrompter{}
}

type filePrompter struct {
	base string
	mode string
}

func (f *filePrompter) Base() string { return f.base }
func (f *filePrompter) Mode() string { return f.mode }

type builtinPrompter struct{}

func (b *builtinPrompter) Base() string { return "" }
func (b *builtinPrompter) Mode() string { return "" }

type PromptFile struct {
	Base string `json:"base"`
	Mode string `json:"mode"`
}
