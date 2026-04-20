package observability

import (
	"fmt"
	"regexp"
	"strings"
)

const defaultRedactionReplacement = "[REDACTED]"

// RedactionRule 定义一条脱敏规则。
type RedactionRule struct {
	Name           string
	FieldNames     []string
	PathPatterns   []string
	ValuePatterns  []string
	OutputPatterns []string
	Replacement    string
}

type compiledRule struct {
	name           string
	fieldNames     map[string]struct{}
	pathPatterns   []*regexp.Regexp
	valuePatterns  []*regexp.Regexp
	outputPatterns []*regexp.Regexp
	replacement    string
}

// Redactor 负责对属性、Prompt 与输出内容做脱敏处理。
type Redactor struct {
	rules []compiledRule
}

// DefaultRedactionRules 返回默认脱敏规则。
func DefaultRedactionRules() []RedactionRule {
	return []RedactionRule{
		{
			Name:        "default-sensitive-headers",
			FieldNames:  []string{"authorization", "x-api-key", "api-key", "api_key", "cookie", "set-cookie"},
			Replacement: defaultRedactionReplacement,
		},
		{
			Name:          "default-secret-patterns",
			ValuePatterns: []string{`(?i)bearer\s+[a-z0-9._\-]+`, `(?i)sk-[a-z0-9]+`, `(?i)api[_-]?key["'=:\s]+[a-z0-9._\-]+`},
			Replacement:   defaultRedactionReplacement,
		},
	}
}

// NewRedactor 创建 Redactor。
func NewRedactor(rules []RedactionRule) (*Redactor, error) {
	compiled := make([]compiledRule, 0, len(rules))
	for _, rule := range rules {
		compiledRule, err := compileRule(rule)
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, compiledRule)
	}
	return &Redactor{rules: compiled}, nil
}

// RedactValue 对任意值执行脱敏。
func (r *Redactor) RedactValue(path string, value any) any {
	if r == nil {
		return value
	}
	switch v := value.(type) {
	case string:
		return r.redactString(path, v)
	case map[string]any:
		cloned := make(map[string]any, len(v))
		for key, raw := range v {
			nextPath := key
			if path != "" {
				nextPath = path + "." + key
			}
			cloned[key] = r.RedactValue(nextPath, raw)
		}
		return cloned
	case []any:
		cloned := make([]any, len(v))
		for i, item := range v {
			cloned[i] = r.RedactValue(path, item)
		}
		return cloned
	case []string:
		cloned := make([]string, len(v))
		for i, item := range v {
			redacted := r.RedactValue(path, item)
			cloned[i] = stringifyValue(redacted)
		}
		return cloned
	default:
		return value
	}
}

func (r *Redactor) redactString(path, value string) string {
	for _, rule := range r.rules {
		if rule.matchesKey(path) || rule.matchesPath(path) {
			return rule.replacement
		}
	}
	redacted := value
	for _, rule := range r.rules {
		for _, pattern := range rule.valuePatterns {
			redacted = pattern.ReplaceAllString(redacted, rule.replacement)
		}
		for _, pattern := range rule.outputPatterns {
			redacted = pattern.ReplaceAllString(redacted, rule.replacement)
		}
	}
	return redacted
}

func compileRule(rule RedactionRule) (compiledRule, error) {
	compiled := compiledRule{
		name:        rule.Name,
		fieldNames:  make(map[string]struct{}, len(rule.FieldNames)),
		replacement: rule.Replacement,
	}
	if compiled.replacement == "" {
		compiled.replacement = defaultRedactionReplacement
	}
	for _, fieldName := range rule.FieldNames {
		fieldName = strings.ToLower(strings.TrimSpace(fieldName))
		if fieldName == "" {
			continue
		}
		compiled.fieldNames[fieldName] = struct{}{}
	}
	var err error
	compiled.pathPatterns, err = compilePatterns(rule.PathPatterns)
	if err != nil {
		return compiledRule{}, fmt.Errorf("compile path patterns for rule %s: %w", rule.Name, err)
	}
	compiled.valuePatterns, err = compilePatterns(rule.ValuePatterns)
	if err != nil {
		return compiledRule{}, fmt.Errorf("compile value patterns for rule %s: %w", rule.Name, err)
	}
	compiled.outputPatterns, err = compilePatterns(rule.OutputPatterns)
	if err != nil {
		return compiledRule{}, fmt.Errorf("compile output patterns for rule %s: %w", rule.Name, err)
	}
	return compiled, nil
}

func compilePatterns(patterns []string) ([]*regexp.Regexp, error) {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, re)
	}
	return compiled, nil
}

func (r compiledRule) matchesKey(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	parts := strings.Split(path, ".")
	key := strings.ToLower(parts[len(parts)-1])
	_, ok := r.fieldNames[key]
	return ok
}

func (r compiledRule) matchesPath(path string) bool {
	for _, pattern := range r.pathPatterns {
		if pattern.MatchString(path) {
			return true
		}
	}
	return false
}
