package review

import (
	"context"
	"regexp"
	"sort"
	"strings"
)

type lineRule struct {
	id          string
	cwe         string
	severity    Severity
	title       string
	explanation string
	fix         string
	test        string
	confidence  float64
	match       func(string) bool
}

// RuleReviewer 是一个轻量、确定性的审查基线。
// 它让旁路在没有 LLM 时也能工作，并为后续 Evaluation Harness 提供可复现的对照组；
// 它并不等同于最终的生产级安全 Reviewer。
type RuleReviewer struct {
	rules []lineRule
}

// NewRuleReviewer 创建内置规则 Reviewer。
// 当前规则只覆盖少量高信号场景，避免为了数量加入大量不可解释的误报规则。
func NewRuleReviewer() *RuleReviewer {
	hardcodedSecret := regexp.MustCompile(`(?i)(api[_-]?key|secret|password|access[_-]?token)\s*(?::=|=|:)\s*["'][^"']{4,}["']`)
	return &RuleReviewer{rules: []lineRule{
		{
			id: "SEC-HARDCODED-SECRET", cwe: "CWE-798", severity: SeverityHigh,
			title:       "Hard-coded credential in added code",
			explanation: "Credentials committed to source control can be recovered from repository history and reused by unauthorized parties.",
			fix:         "Load the credential from a secret manager or environment injection and rotate the exposed value.",
			test:        "Add a secret-scanning check and verify the application starts with an injected credential.",
			confidence:  0.98,
			match:       func(line string) bool { return hardcodedSecret.MatchString(line) },
		},
		{
			id: "SEC-TLS-SKIP-VERIFY", cwe: "CWE-295", severity: SeverityHigh,
			title:       "TLS certificate verification disabled",
			explanation: "Disabling certificate verification allows a network attacker to impersonate the remote service.",
			fix:         "Remove InsecureSkipVerify and configure the expected system or private CA trust roots.",
			test:        "Verify that an untrusted test certificate is rejected and the trusted certificate succeeds.",
			confidence:  0.99,
			match: func(line string) bool {
				compact := strings.ReplaceAll(line, " ", "")
				compact = strings.ReplaceAll(compact, "\t", "")
				return strings.Contains(compact, "InsecureSkipVerify:true")
			},
		},
		{
			id: "SEC-SHELL-COMMAND", cwe: "CWE-78", severity: SeverityMedium,
			title:       "Shell interpreter introduced in command execution",
			explanation: "Passing dynamically assembled data through a shell can turn untrusted input into executable syntax.",
			fix:         "Invoke the target executable directly with a fixed argument vector and validate any user-controlled values.",
			test:        "Exercise arguments containing shell metacharacters and assert they are treated as plain data.",
			confidence:  0.86,
			match: func(line string) bool {
				return (strings.Contains(line, "exec.Command(") || strings.Contains(line, "exec.CommandContext(")) &&
					(strings.Contains(line, `"sh", "-c"`) || strings.Contains(line, `"bash", "-c"`))
			},
		},
	}}
}

// Name 返回带版本的 Reviewer 名称，报告与评测结果可据此追踪具体实现。
func (r *RuleReviewer) Name() string { return "deterministic-rules/v1" }

// Review 只检查新增行：PR Review 应对本次引入的问题负责，不重复报告仓库既有代码。
func (r *RuleReviewer) Review(ctx context.Context, _ Request, files []ChangedFile) ([]Finding, error) {
	findings := make([]Finding, 0)
	for _, file := range files {
		for _, hunk := range file.Hunks {
			for _, line := range hunk.Lines {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if line.Kind != LineAdded {
					continue
				}
				for _, rule := range r.rules {
					if !rule.match(line.Content) {
						continue
					}
					findings = append(findings, Finding{
						RuleID: rule.id, CWE: rule.cwe, Severity: rule.severity,
						Title: rule.title, Explanation: rule.explanation, Path: file.Path(),
						StartLine: line.NewLine, EndLine: line.NewLine, Evidence: strings.TrimSpace(line.Content),
						Fix: rule.fix, Test: rule.test, Confidence: rule.confidence,
					})
				}
			}
		}
	}
	// 固定输出顺序，确保相同输入可生成字节级稳定的报告，便于快照和离线评测。
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Path != findings[j].Path {
			return findings[i].Path < findings[j].Path
		}
		if findings[i].StartLine != findings[j].StartLine {
			return findings[i].StartLine < findings[j].StartLine
		}
		return findings[i].RuleID < findings[j].RuleID
	})
	return findings, nil
}
