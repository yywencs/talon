package review

import (
	"context"
	"fmt"
	"strings"
)

// Service 负责串联 Diff 解析、Reviewer 执行和报告聚合，是代码审查旁路的应用服务。
type Service struct {
	reviewer Reviewer
}

// NewService 使用指定 Reviewer 创建审查服务，调用方可在此替换规则、LLM 或多 Agent 实现。
func NewService(reviewer Reviewer) *Service {
	return &Service{reviewer: reviewer}
}

// Review 执行一次完整代码审查，并输出稳定的版本化 Report。
func (s *Service) Review(ctx context.Context, request Request) (Report, error) {
	if s == nil || s.reviewer == nil {
		return Report{}, fmt.Errorf("review: reviewer is required")
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if strings.TrimSpace(request.Diff) == "" {
		return Report{}, fmt.Errorf("review: diff is required")
	}

	files, err := ParseUnifiedDiff(request.Diff)
	if err != nil {
		return Report{}, err
	}
	findings, err := s.reviewer.Review(ctx, request, files)
	if err != nil {
		return Report{}, fmt.Errorf("review: %s: %w", s.reviewer.Name(), err)
	}
	// JSON 中固定输出 [] 而非 null，简化 API、前端和评测器的消费逻辑。
	if findings == nil {
		findings = make([]Finding, 0)
	}

	summary := summarize(findings)
	return Report{
		SchemaVersion: SchemaVersion,
		Repository:    request.Repository, PullRequest: request.PullRequest,
		BaseSHA: request.BaseSHA, HeadSHA: request.HeadSHA,
		Risk: risk(summary), FilesReviewed: len(files), Reviewer: s.reviewer.Name(),
		Summary: summary, Findings: findings,
	}, nil
}

func summarize(findings []Finding) Summary {
	summary := Summary{Total: len(findings)}
	for _, finding := range findings {
		switch finding.Severity {
		case SeverityCritical:
			summary.Critical++
		case SeverityHigh:
			summary.High++
		case SeverityMedium:
			summary.Medium++
		case SeverityLow:
			summary.Low++
		}
	}
	return summary
}

func risk(summary Summary) string {
	// 整体风险取所有 Finding 中的最高等级；没有问题时显式返回 none。
	switch {
	case summary.Critical > 0:
		return string(SeverityCritical)
	case summary.High > 0:
		return string(SeverityHigh)
	case summary.Medium > 0:
		return string(SeverityMedium)
	case summary.Low > 0:
		return string(SeverityLow)
	default:
		return "none"
	}
}
