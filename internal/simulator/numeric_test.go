package simulator

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/scenario"
	"math"
	"strconv"
	"testing"
	"time"
)

func TestExactIntegerConversions(t *testing.T) {
	for _, v := range []any{int(12), int8(12), int16(12), int32(12), int64(12), uint(12), uint8(12), uint16(12), uint32(12), uint64(12), float32(12), float64(12), json.Number("12")} {
		i, ok := asInt64(v)
		require.True(t, ok)
		require.Equal(t, int64(12), i)
	}
	for _, v := range []any{json.Number("9223372036854775808"), json.Number("-9223372036854775809"), json.Number("12.5"), json.Number("bad"), uint64(math.MaxUint64), float64(12.5), math.Inf(1), math.NaN(), float64(0x1p63), "12", nil} {
		_, ok := asInt64(v)
		require.False(t, ok, "%v", v)
	}
	for _, v := range []int64{9007199254740993, math.MaxInt64, math.MinInt64} {
		i, ok := asInt64(json.Number(strconv.FormatInt(v, 10)))
		require.True(t, ok)
		require.Equal(t, v, i)
	}
	f, ok := asFloat(json.Number("12.5"))
	require.True(t, ok)
	require.Equal(t, 12.5, f)
}

func TestPoolGenerationPresenceFollowsToolDefinition(t *testing.T) {
	item := findTestCase(t, "connection-recovery-two-cycles-001")
	for _, required := range []bool{true, false} {
		for _, present := range []bool{true, false} {
			document := item.Scenario
			document.RemediationTools = append([]scenario.ToolDefinition(nil), document.RemediationTools...)
			if !required {
				for i, tool := range document.RemediationTools {
					if tool.Name == "refresh_provider_connection" {
						arguments := []string{}
						for _, arg := range tool.Arguments {
							if arg != "expected_pool_generation" {
								arguments = append(arguments, arg)
							}
						}
						document.RemediationTools[i].Arguments = arguments
					}
				}
			}
			sim, e := New(document)
			require.NoError(t, e)
			require.NoError(t, sim.Advance(context.Background(), 5*time.Minute))
			args := map[string]any{"provider_id": "provider-search-a"}
			if present {
				args["expected_pool_generation"] = "invalid"
			}
			op, e := sim.ExecuteRemediation(context.Background(), platform.RemediationRequest{IncidentID: document.Metadata.ID, ToolName: "refresh_provider_connection", Arguments: args, IdempotencyKey: "presence"})
			if required || present {
				require.ErrorIs(t, e, platform.ErrPreconditionFailed)
				require.Equal(t, platform.OperationRejected, op.Status)
			} else {
				require.NoError(t, e)
				require.Equal(t, platform.OperationPending, op.Status)
			}
		}
	}
}
