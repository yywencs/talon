package agentreview

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/wen/opentalon/internal/review"
)

var errEmptyModelResponse = errors.New("agentreview: model returned an empty response")

type modelOutput struct {
	Findings []review.Finding `json:"findings"`
}

func decodeAndValidateFindings(raw string) ([]review.Finding, error) {
	raw = stripJSONFence(strings.TrimSpace(raw))
	if raw == "" {
		return nil, errEmptyModelResponse
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var output modelOutput
	if err := decoder.Decode(&output); err != nil {
		return nil, fmt.Errorf("agentreview: decode model JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("agentreview: model returned multiple JSON values")
		}
		return nil, fmt.Errorf("agentreview: decode trailing model output: %w", err)
	}
	if output.Findings == nil {
		output.Findings = make([]review.Finding, 0)
	}
	return output.Findings, nil
}

func validateFindings(findings []review.Finding, files []review.ChangedFile) ([]review.Finding, error) {
	locations := changedLocations(files)
	seen := make(map[string]struct{})
	validated := make([]review.Finding, 0, len(findings))
	for index, finding := range findings {
		if strings.TrimSpace(finding.RuleID) == "" {
			finding.RuleID = "AGENT-REVIEW"
		}
		if err := validateFinding(finding, locations); err != nil {
			return nil, fmt.Errorf("agentreview: finding %d: %w", index, err)
		}
		key := fmt.Sprintf("%s\x00%d\x00%d\x00%s", finding.Path, finding.StartLine, finding.EndLine, finding.Title)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		validated = append(validated, finding)
	}
	sort.SliceStable(validated, func(i, j int) bool {
		if validated[i].Path != validated[j].Path {
			return validated[i].Path < validated[j].Path
		}
		if validated[i].StartLine != validated[j].StartLine {
			return validated[i].StartLine < validated[j].StartLine
		}
		return validated[i].Title < validated[j].Title
	})
	return validated, nil
}

type fileLocations struct {
	lines map[int]struct{}
}

func changedLocations(files []review.ChangedFile) map[string]fileLocations {
	result := make(map[string]fileLocations, len(files))
	for _, file := range files {
		location := fileLocations{lines: make(map[int]struct{})}
		for _, hunk := range file.Hunks {
			for _, line := range hunk.Lines {
				if line.OldLine > 0 {
					location.lines[line.OldLine] = struct{}{}
				}
				if line.NewLine > 0 {
					location.lines[line.NewLine] = struct{}{}
				}
			}
		}
		result[file.Path()] = location
	}
	return result
}

func validateFinding(finding review.Finding, locations map[string]fileLocations) error {
	location, exists := locations[finding.Path]
	if !exists {
		return fmt.Errorf("path %q is not in the reviewed diff", finding.Path)
	}
	if strings.TrimSpace(finding.CWE) == "" || strings.TrimSpace(finding.Title) == "" ||
		strings.TrimSpace(finding.Explanation) == "" || strings.TrimSpace(finding.Evidence) == "" ||
		strings.TrimSpace(finding.Fix) == "" || strings.TrimSpace(finding.Test) == "" {
		return fmt.Errorf("cwe, title, explanation, evidence, fix and test are required")
	}
	switch finding.Severity {
	case review.SeverityCritical, review.SeverityHigh, review.SeverityMedium, review.SeverityLow:
	default:
		return fmt.Errorf("unsupported severity %q", finding.Severity)
	}
	if finding.Confidence <= 0 || finding.Confidence > 1 {
		return fmt.Errorf("confidence %.4f is outside (0,1]", finding.Confidence)
	}
	if finding.StartLine <= 0 || finding.EndLine < finding.StartLine {
		return fmt.Errorf("invalid line range %d-%d", finding.StartLine, finding.EndLine)
	}
	if _, ok := location.lines[finding.StartLine]; !ok {
		return fmt.Errorf("start line %d is not visible in diff for %q", finding.StartLine, finding.Path)
	}
	if _, ok := location.lines[finding.EndLine]; !ok {
		return fmt.Errorf("end line %d is not visible in diff for %q", finding.EndLine, finding.Path)
	}
	return nil
}

func stripJSONFence(raw string) string {
	if !strings.HasPrefix(raw, "```") {
		return raw
	}
	firstNewline := strings.IndexByte(raw, '\n')
	lastFence := strings.LastIndex(raw, "```")
	if firstNewline < 0 || lastFence <= firstNewline {
		return raw
	}
	return strings.TrimSpace(raw[firstNewline+1 : lastFence])
}
