package scenario

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateActionBehaviorAcceptsValidWhen(t *testing.T) {
	require.NoError(t, validateActionBehavior(map[string]map[string]any{
		"request_probe": {
			"attempts": []any{
				map[string]any{"steps": []any{map[string]any{"success_rate": 0.99}}},
				map[string]any{
					"when":  map[string]any{"after_world_effect": "recreate_provider_connection_pool"},
					"steps": []any{map[string]any{"success_rate": 0.99}},
				},
				map[string]any{
					"when": map[string]any{"after_event": map[string]any{
						"event": "provider.endpoint.change", "target": "provider-fetch-a", "cause": "standby_node_promoted_by_platform",
					}},
				},
			},
		},
	}))
}

func TestValidateActionBehaviorRejectsMalformedWhen(t *testing.T) {
	tests := []struct {
		name     string
		when     any
		contains string
	}{
		{"未知谓词键", map[string]any{"before_event": "x"}, "unsupported key"},
		{"空 when", map[string]any{}, "after_event or after_world_effect"},
		{"after_event 非映射", "provider.endpoint.change", "must be a mapping"},
		{"after_event 无有效字段", map[string]any{"after_event": map[string]any{"event": " "}}, "at least one non-empty"},
		{"after_world_effect 空", map[string]any{"after_world_effect": " "}, "non-empty remediation tool name"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateActionBehavior(map[string]map[string]any{
				"request_probe": {"attempts": []any{map[string]any{"when": test.when}}},
			})
			require.ErrorContains(t, err, test.contains)
		})
	}
}
