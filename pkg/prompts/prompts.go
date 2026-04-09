package prompts

import (
	"bytes"
	"os"
	"path/filepath"
	"text/template"
)

type Prompter interface {
	Base() string
}

type Registry struct {
	fsDir    string
	template *template.Template
	data     any
}

type PromptData struct {
	AgentSkills string
}

func NewRegistry(fsDir string) *Registry {
	return &Registry{fsDir: fsDir}
}

func (r *Registry) SetData(data any) {
	r.data = data
}

func (r *Registry) Get(name string) Prompter {
	path := filepath.Join(r.fsDir, name+".md")
	if r.fsDir == "" {
		return &emptyPrompter{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return &emptyPrompter{}
	}
	tmpl, err := template.New(name).Parse(string(data))
	if err != nil {
		return &emptyPrompter{}
	}
	return &filePrompter{tmpl: tmpl, data: r.data}
}

type filePrompter struct {
	tmpl *template.Template
	data any
}

func (f *filePrompter) Base() string {
	var buf bytes.Buffer
	if f.tmpl.Execute(&buf, f.data) != nil {
		return ""
	}
	return buf.String()
}

type emptyPrompter struct{}

func (e *emptyPrompter) Base() string { return "" }
