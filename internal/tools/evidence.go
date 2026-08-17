package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/wen/opentalon/internal/platform"
)

// evidenceResponse annotates successful read results with stable semantic IDs.
// The IDs describe facts already present in data; they do not expose hidden
// scenario expectations or causes.
func evidenceResponse[T any](data T, err error) response[T] {
	result := platformResponse(data, err)
	if err == nil {
		result.EvidenceIDs = canonicalEvidenceIDs(any(data))
	}
	return result
}

func canonicalEvidenceIDs(value any) []string {
	ids := make(map[string]struct{})
	add := func(value string) {
		if value != "" {
			ids[value] = struct{}{}
		}
	}
	switch values := value.(type) {
	case platform.MetricResult:
		for _, point := range values.Points {
			switch point.Name {
			case platform.MetricErrorRate:
				if point.Dimensions["route_id"] != "" {
					add("metric.error_rate_by_route")
				}
			case platform.MetricAuthenticationErrorRate:
				add("metric.authentication_error_rate")
			case platform.MetricConnectionErrorRate:
				add("metric.connection_error_rate_by_route")
			}
		}
	case []platform.LogEntry:
		for _, entry := range values {
			if code := canonicalSegment(entry.Code); code != "" {
				add("log." + code)
			}
		}
	case []platform.TraceRecord:
		for _, trace := range values {
			if sent, ok := trace.Attributes["provider_request_sent"].(bool); ok && !sent {
				add("trace.provider_request_not_sent")
			}
			if number(trace.Attributes["provider_status_code"]) == 401 {
				add("trace.provider_status_401")
			}
			if peer, _ := trace.Attributes["peer_address"].(string); strings.TrimSpace(peer) != "" && trace.TerminalSpan == "provider.connect" {
				add("trace.peer_address_observed")
			}
		}
	case []platform.Provider:
		for _, provider := range values {
			if provider.Health == platform.ProviderHealthy {
				add("provider.endpoint_healthy")
			}
		}
	case []platform.Route:
		for _, route := range values {
			if route.UnavailableReason == "schema_not_compatible" {
				add("route.no_compatible_fallback")
			}
		}
	case []platform.ChangeRecord:
		for _, change := range values {
			if change.Kind == "mapping_publish" {
				if version, _ := change.Attributes["version"].(string); version != "" {
					add("change." + canonicalSegment(version) + "_publish")
				}
			}
		}
	case []platform.CredentialMetadata:
		for _, credential := range values {
			if credential.Status == "invalid" {
				add("credential.status_invalid")
			}
		}
	case []platform.ConnectionMetadata:
		if len(values) > 0 {
			add("connection.resolver_cache_generation")
		}
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func canonicalSegment(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	var result strings.Builder
	underscore := false
	for _, item := range value {
		if unicode.IsLetter(item) || unicode.IsDigit(item) {
			result.WriteRune(item)
			underscore = false
		} else if result.Len() > 0 && !underscore {
			result.WriteByte('_')
			underscore = true
		}
	}
	return strings.Trim(result.String(), "_")
}

func number(value any) int64 {
	switch item := value.(type) {
	case int:
		return int64(item)
	case int8:
		return int64(item)
	case int16:
		return int64(item)
	case int32:
		return int64(item)
	case int64:
		return item
	case uint:
		return int64(item)
	case uint8:
		return int64(item)
	case uint16:
		return int64(item)
	case uint32:
		return int64(item)
	case uint64:
		if item <= uint64(^uint64(0)>>1) {
			return int64(item)
		}
		return 0
	case float32:
		return int64(item)
	case float64:
		return int64(item)
	case json.Number:
		result, _ := item.Int64()
		return result
	case fmt.Stringer:
		var result int64
		_, _ = fmt.Sscan(item.String(), &result)
		return result
	default:
		return 0
	}
}
