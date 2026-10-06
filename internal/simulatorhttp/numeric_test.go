package simulatorhttp

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/scenario"
	"github.com/wen/opentalon/internal/simulator"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestPoolGenerationPreconditionsDirectAndHTTP(t *testing.T) {
	cases := []struct {
		name       string
		value      any
		generation int
		want       error
	}{
		{"match", 12, 12, nil}, {"mismatch", 999, 12, platform.ErrConflict},
		{"string", "12", 12, platform.ErrPreconditionFailed}, {"null", nil, 12, platform.ErrPreconditionFailed}, {"bool", true, 12, platform.ErrPreconditionFailed}, {"fraction", 12.5, 12, platform.ErrPreconditionFailed}, {"number_fraction", json.Number("12.5"), 12, platform.ErrPreconditionFailed}, {"overflow", json.Number("9223372036854775808"), 12, platform.ErrPreconditionFailed},
	}
	if strconv.IntSize == 64 {
		large := int64(9007199254740993)
		cases = append(cases, struct {
			name       string
			value      any
			generation int
			want       error
		}{"large_match", json.Number("9007199254740993"), int(large), nil}, struct {
			name       string
			value      any
			generation int
			want       error
		}{"large_adjacent_conflict", json.Number("9007199254740992"), int(large), platform.ErrConflict})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, transport := range []string{"direct", "http"} {
				t.Run(transport, func(t *testing.T) {
					ctx := context.Background()
					ds, e := scenario.LoadDataset("../../data/toolops-v1")
					require.NoError(t, e)
					item, ok := ds.Find("connection-recovery-two-cycles-001")
					require.True(t, ok)
					document := item.Scenario
					connection := document.InitialState.Connection["provider-search-a"]
					connection.PoolGeneration = tc.generation
					document.InitialState.Connection["provider-search-a"] = connection
					sim, e := simulator.New(document)
					require.NoError(t, e)
					require.NoError(t, sim.Advance(ctx, 5*time.Minute))
					require.Equal(t, tc.generation, sim.Snapshot().Connections["provider-search-a"].PoolGeneration)
					var p platform.ToolOpsPlatform = sim
					if transport == "http" {
						s, e := NewServer(sim, ds.Version)
						require.NoError(t, e)
						h := httptest.NewServer(s)
						defer h.Close()
						c, e := NewClient(h.URL, time.Second)
						require.NoError(t, e)
						p = bind(t, c, s, "numeric-run", 1)
					}
					op, e := p.ExecuteRemediation(ctx, platform.RemediationRequest{IncidentID: document.Metadata.ID, ToolName: "refresh_provider_connection", Arguments: map[string]any{"provider_id": "provider-search-a", "expected_pool_generation": tc.value}, IdempotencyKey: "generation"})
					if tc.want == nil {
						require.NoError(t, e)
						require.Equal(t, platform.OperationPending, op.Status)
					} else {
						require.ErrorIs(t, e, tc.want)
						require.Equal(t, platform.OperationRejected, op.Status)
					}
					require.Equal(t, tc.generation, sim.Snapshot().Connections["provider-search-a"].PoolGeneration)
				})
			}
		})
	}
}
