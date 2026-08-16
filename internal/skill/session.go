package skill

import (
	"fmt"
	"strings"
	"sync"
)

const DefaultMaxActive = 2

// EvidenceVerifier 由外层 Harness 注入，用于确认 Skill 变更引用了本次运行中
// 已成功产生的公开工具证据。Registry 和 Session 不依赖具体 Artifact 实现。
type EvidenceVerifier func([]string) error

// Change 是 load_skill 或 unload_skill 成功后的可审计结果。
type Change struct {
	Action       string   `json:"action"`
	Name         string   `json:"name"`
	Digest       string   `json:"digest"`
	Reason       string   `json:"reason"`
	EvidenceRefs []string `json:"evidence_refs"`
	ActiveSkills []string `json:"active_skills"`
}

// Session 保存单个 Incident 当前按需加载的 Skill 集合。
type Session struct {
	mu       sync.RWMutex
	registry *Registry
	max      int
	verify   EvidenceVerifier
	active   []Definition
	byName   map[string]struct{}
}

func NewSession(registry *Registry, maxActive int, verify EvidenceVerifier) (*Session, error) {
	if registry == nil {
		return nil, fmt.Errorf("skill registry is required")
	}
	if maxActive == 0 {
		maxActive = DefaultMaxActive
	}
	if maxActive < 0 {
		return nil, fmt.Errorf("max active skills must not be negative")
	}
	return &Session{registry: registry, max: maxActive, verify: verify, byName: make(map[string]struct{})}, nil
}

func (s *Session) Catalog() []CatalogEntry {
	if s == nil || s.registry == nil {
		return nil
	}
	return s.registry.Catalog()
}

func (s *Session) Active() []Definition {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Definition, len(s.active))
	for index, definition := range s.active {
		result[index] = cloneDefinition(definition)
	}
	return result
}

func (s *Session) Activate(name, reason string, evidenceRefs []string) (Change, error) {
	if s == nil || s.registry == nil {
		return Change{}, fmt.Errorf("skill session is not initialized")
	}
	name, reason, evidenceRefs, err := validateChangeInput(name, reason, evidenceRefs)
	if err != nil {
		return Change{}, err
	}
	if s.verify != nil {
		if err := s.verify(evidenceRefs); err != nil {
			return Change{}, fmt.Errorf("verify Skill evidence: %w", err)
		}
	}
	definition, found := s.registry.Get(name)
	if !found {
		return Change{}, fmt.Errorf("skill %q is not installed", name)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byName[name]; exists {
		return Change{}, fmt.Errorf("skill %q is already active", name)
	}
	if len(s.active) >= s.max {
		return Change{}, fmt.Errorf("active Skill limit %d reached; unload a disproven Skill first", s.max)
	}
	s.active = append(s.active, definition)
	s.byName[name] = struct{}{}
	return s.changeLocked("loaded", definition, reason, evidenceRefs), nil
}

func (s *Session) Deactivate(name, reason string, evidenceRefs []string) (Change, error) {
	if s == nil || s.registry == nil {
		return Change{}, fmt.Errorf("skill session is not initialized")
	}
	name, reason, evidenceRefs, err := validateChangeInput(name, reason, evidenceRefs)
	if err != nil {
		return Change{}, err
	}
	if s.verify != nil {
		if err := s.verify(evidenceRefs); err != nil {
			return Change{}, fmt.Errorf("verify Skill evidence: %w", err)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byName[name]; !exists {
		return Change{}, fmt.Errorf("skill %q is not active", name)
	}
	var removed Definition
	for index, definition := range s.active {
		if definition.Name != name {
			continue
		}
		removed = definition
		s.active = append(s.active[:index], s.active[index+1:]...)
		break
	}
	delete(s.byName, name)
	return s.changeLocked("unloaded", removed, reason, evidenceRefs), nil
}

func (s *Session) changeLocked(action string, definition Definition, reason string, evidenceRefs []string) Change {
	names := make([]string, len(s.active))
	for index := range s.active {
		names[index] = s.active[index].Name
	}
	return Change{Action: action, Name: definition.Name, Digest: definition.Digest, Reason: reason,
		EvidenceRefs: append([]string(nil), evidenceRefs...), ActiveSkills: names}
}

func validateChangeInput(name, reason string, evidenceRefs []string) (string, string, []string, error) {
	name = strings.TrimSpace(name)
	reason = strings.TrimSpace(reason)
	if name == "" {
		return "", "", nil, fmt.Errorf("skill name is required")
	}
	if reason == "" {
		return "", "", nil, fmt.Errorf("Skill change reason is required")
	}
	if len([]rune(reason)) > 1024 {
		return "", "", nil, fmt.Errorf("Skill change reason exceeds 1024 characters")
	}
	if len(evidenceRefs) > 8 {
		return "", "", nil, fmt.Errorf("Skill change accepts at most 8 evidence references")
	}
	refs := make([]string, 0, len(evidenceRefs))
	seen := make(map[string]struct{}, len(evidenceRefs))
	for _, ref := range evidenceRefs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if len(ref) > 256 {
			return "", "", nil, fmt.Errorf("evidence reference exceeds 256 bytes")
		}
		if _, duplicate := seen[ref]; duplicate {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		return "", "", nil, fmt.Errorf("at least one evidence reference is required")
	}
	return name, reason, refs, nil
}

func cloneDefinition(value Definition) Definition {
	value.AllowedTools = append([]string(nil), value.AllowedTools...)
	return value
}
