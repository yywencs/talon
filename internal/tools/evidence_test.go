package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
	assert.Equal(t, []string{"trace.peer_address_observed"}, canonicalEvidenceIDs([]platform.TraceRecord{{
		TerminalSpan: "provider.connect", Attributes: map[string]any{"peer_address": "10.0.0.1:443"},
	}}))
	assert.Equal(t, []string{"change.mapping_v2_publish"}, canonicalEvidenceIDs([]platform.ChangeRecord{{
		Kind: "mapping_publish", Attributes: map[string]any{"version": "mapping-v2"},
	}}))
	assert.Equal(t, []string{"credential.status_invalid"}, canonicalEvidenceIDs([]platform.CredentialMetadata{{Status: "invalid"}}))
	assert.Equal(t, []string{"route.no_compatible_fallback"}, canonicalEvidenceIDs([]platform.Route{{UnavailableReason: "schema_not_compatible"}}))
}
