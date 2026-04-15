package tool

import (
	"context"
	"encoding/json"

	"github.com/wen/opentalon/internal/types"
)

type Observation = types.Observation

func toString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case []byte:
		return string(val)
	default:
		b, _ := json.Marshal(val)
		return string(b)
	}
}

type SecurityRisk string

const (
	SecurityRisk_UNKNOWN SecurityRisk = "UNKNOWN"
	SecurityRisk_LOW     SecurityRisk = "LOW"
	SecurityRisk_MEDIUM  SecurityRisk = "MEDIUM"
	SecurityRisk_HIGH    SecurityRisk = "HIGH"
)

func (s SecurityRisk) weight() int {
	switch s {
	case SecurityRisk_LOW:
		return 1
	case SecurityRisk_MEDIUM:
		return 2
	case SecurityRisk_HIGH:
		return 3
	default:
		return 0 // UNKNOWN 或其他非法值
	}
}

func (s SecurityRisk) IsRiskierOrEqual(other SecurityRisk) bool {
	if s == SecurityRisk_UNKNOWN || other == SecurityRisk_UNKNOWN {
		return false
	}
	return s.weight() >= other.weight()
}

func (s SecurityRisk) Color() string {
	switch s {
	case SecurityRisk_LOW:
		return "\033[32m" // Green
	case SecurityRisk_MEDIUM:
		return "\033[33m" // Yellow
	case SecurityRisk_HIGH:
		return "\033[31m" // Red
	default:
		return "\033[37m" // White
	}
}

type ActionMetadata struct {
	Summary      string       `json:"summary" jsonschema:"description=动作摘要"`
	SecurityRisk SecurityRisk `json:"security_risk" jsonschema:"description=风险等级"`
}

type BashAction struct {
	ActionMetadata `json:",inline"`
	Command        string `json:"command"`
}

type Tool interface {
	Name() string
	Description() string
	Execute(ctx context.Context, rawArgs []byte) Observation
}

type BaseTool[A any, O any] struct {
	ToolName string
	ToolDesc string
	Executor func(ctx context.Context, action A) O
}

func (t *BaseTool[A, O]) Execute(ctx context.Context, rawArgs []byte) (Observation, error) {
	var action A
	if err := json.Unmarshal(rawArgs, &action); err != nil {
		return nil, err
	}

	result := t.Executor(ctx, action)

	return NewObservation(result)
}
