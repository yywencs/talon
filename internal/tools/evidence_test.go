package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/platform"
)

func TestCanonicalEvidenceIDsDescribeStructuredFacts(t *testing.T) {
	assert.Equal(t, []string{"metric.error_rate_by_route"}, canonicalEvidenceIDs(platform.MetricResult{
		Points: []platform.MetricPoint{{Name: platform.MetricErrorRate, Dimensions: map[string]string{"route_id": "route-a"}}},
	}))
	assert.Equal(t, []string{"log.invalid_parameter_type"}, canonicalEvidenceIDs([]platform.LogEntry{{Code: "invalid_parameter_type"}}))
	assert.Equal(t, []string{"trace.provider_request_not_sent"}, canonicalEvidenceIDs([]platform.TraceRecord{{
		Attributes: map[string]any{"provider_request_sent": false},
	}}))
	for _, statusCode := range []any{401, int64(401), uint64(401), float64(401), json.Number("401")} {
		assert.Equal(t, []string{"trace.provider_status_401"}, canonicalEvidenceIDs([]platform.TraceRecord{{
			Attributes: map[string]any{"provider_status_code": statusCode},
		}}))
	}
	assert.Equal(t, []string{"trace.peer_address_observed"}, canonicalEvidenceIDs([]platform.TraceRecord{{
		TerminalSpan: "provider.connect", Attributes: map[string]any{"peer_address": "10.0.0.1:443"},
	}}))
	assert.Equal(t, []string{"change.mapping_v2_publish"}, canonicalEvidenceIDs([]platform.ChangeRecord{{
		Kind: "mapping_publish", Attributes: map[string]any{"version": "mapping-v2"},
	}}))
	assert.Equal(t, []string{"credential.status_invalid"}, canonicalEvidenceIDs([]platform.CredentialMetadata{{Status: "invalid"}}))
	assert.Equal(t, []string{"route.no_compatible_fallback"}, canonicalEvidenceIDs([]platform.Route{{UnavailableReason: "schema_not_compatible"}}))
}

func TestCredentialTraceProducesCanonical401Evidence(t *testing.T) {
	service, item := newTestSimulator(t, "credential-revoked-escalation-001")
	require.NoError(t, service.Advance(context.Background(), 4*time.Minute))
	toolSet, err := New(context.Background(), service, item.Scenario.Metadata.ID)
	require.NoError(t, err)
	query, ok := toolSet.Resolve("query_traces")
	require.True(t, ok)

	encoded, err := query.InvokableRun(context.Background(), `{}`)
	require.NoError(t, err)
	var result response[[]platform.TraceRecord]
	require.NoError(t, json.Unmarshal([]byte(encoded), &result))
	assert.Contains(t, result.EvidenceIDs, "trace.provider_status_401")
}
