package simulator

import (
	"fmt"
	"time"

	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/scenario"
)

// advanceToLocked 按发生时间合并处理场景事件、修复完成事件和探测窗口。
// 调用方必须持有 World 的写锁。
func (w *World) advanceToLocked(target time.Time) error {
	for {
		eventAt, hasEvent, err := w.nextEventAtLocked()
		if err != nil {
			return err
		}
		operationID, operationAt, hasOperation := w.nextOperationAtLocked()
		probeID, probeAt, hasProbe := w.nextProbeAtLocked()
		if !hasEvent && !hasOperation && !hasProbe {
			break
		}

		nextAt := eventAt
		kind := "event"
		if !hasEvent {
			nextAt, kind = operationAt, "operation"
			if !hasOperation {
				nextAt, kind = probeAt, "probe"
			}
		}
		if hasOperation && (nextAt.IsZero() || operationAt.Before(nextAt)) {
			nextAt, kind = operationAt, "operation"
		}
		if hasProbe && (nextAt.IsZero() || probeAt.Before(nextAt)) {
			nextAt, kind = probeAt, "probe"
		}
		if nextAt.After(target) {
			break
		}
		w.now = nextAt
		switch kind {
		case "event":
			if err := w.applyTimelineEventLocked(w.timeline[w.nextTimelineEvent]); err != nil {
				return err
			}
			w.nextTimelineEvent++
		case "operation":
			w.completeOperationLocked(operationID)
		case "probe":
			w.processProbeWindowLocked(probeID)
		}
	}
	w.now = target
	return nil
}

func (w *World) nextProbeAtLocked() (string, time.Time, bool) {
	var selectedID string
	var selectedAt time.Time
	for id, session := range w.probes {
		if selectedID == "" || session.dueAt.Before(selectedAt) ||
			(session.dueAt.Equal(selectedAt) && id < selectedID) {
			selectedID = id
			selectedAt = session.dueAt
		}
	}
	return selectedID, selectedAt, selectedID != ""
}

func (w *World) nextEventAtLocked() (time.Time, bool, error) {
	if w.nextTimelineEvent >= len(w.timeline) {
		return time.Time{}, false, nil
	}
	delay, err := time.ParseDuration(w.timeline[w.nextTimelineEvent].At)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("parse timeline event time: %w", err)
	}
	return w.startAt.Add(delay), true, nil
}

func (w *World) nextOperationAtLocked() (string, time.Time, bool) {
	var selectedID string
	var selectedAt time.Time
	for id, pending := range w.pending {
		if selectedID == "" || pending.dueAt.Before(selectedAt) ||
			(pending.dueAt.Equal(selectedAt) && id < selectedID) {
			selectedID = id
			selectedAt = pending.dueAt
		}
	}
	return selectedID, selectedAt, selectedID != ""
}

func (w *World) applyTimelineEventLocked(event scenario.TimelineEvent) error {
	effect := asMap(event.Values["internal_effect"])
	w.applyTrafficEffectLocked(effect, event.Target)

	switch event.Event {
	case "config.publish":
		w.publishConfigLocked(asString(event.Values["version"]))
	case "account.auth.fail":
		w.applyCredentialTelemetryLocked(event.Target, asMap(asMap(event.Values["telemetry"])["credential_metadata"]))
	case "provider.endpoint.change":
		provider, ok := w.providers[event.Target]
		if !ok {
			return fmt.Errorf("timeline references unknown provider %q", event.Target)
		}
		if endpoint := asString(event.Values["endpoint"]); endpoint != "" {
			provider.Endpoint = endpoint
		}
		w.providers[event.Target] = provider
	default:
		return fmt.Errorf("unsupported timeline event %q", event.Event)
	}

	w.applyProtectionLocked(event.Target, asString(effect["route"]))
	w.ingestTelemetryLocked(event.Target, asMap(event.Values["telemetry"]))
	return nil
}

func (w *World) publishConfigLocked(version string) {
	if version == "" {
		return
	}
	for id, config := range w.configs {
		config.Active = false
		w.configs[id] = config
	}
	config := w.configs[version]
	config.ID = version
	config.Active = true
	w.configs[version] = config
}

func (w *World) applyTrafficEffectLocked(effect map[string]any, target string) {
	if value, ok := asFloat(effect["success_rate"]); ok {
		w.traffic.SuccessRate = value
	}
	if value, ok := asInt64(effect["latency_p95_ms"]); ok {
		w.traffic.LatencyP95MS = value
	}
	if value, ok := asFloat(effect["cost_per_success"]); ok {
		w.traffic.CostPerSuccess = &value
	}
	if errorType := asString(effect["error_type"]); errorType != "" {
		w.lastErrorType = errorType
	}
	if cleared := asString(effect["error_type_cleared"]); cleared != "" && cleared == w.lastErrorType {
		w.lastErrorType = ""
	}
	w.appendTrafficMetricsLocked(target, asString(effect["route"]))
}

func (w *World) appendTrafficMetricsLocked(target, routeID string) {
	serviceID, toolName := w.initialServiceAndTool()
	dimensions := map[string]string{"incident_id": w.scenarioID, "service_id": serviceID, "tool_name": toolName}
	if routeID == "" {
		for id, route := range w.routes {
			if route.ProviderID == target {
				routeID = id
				break
			}
		}
	}
	if routeID != "" {
		dimensions["route_id"] = routeID
		if route, ok := w.routes[routeID]; ok {
			dimensions["provider_id"] = route.ProviderID
		}
	} else if _, ok := w.providers[target]; ok {
		dimensions["provider_id"] = target
	}
	w.metrics = append(w.metrics,
		platform.MetricPoint{At: w.now, Name: platform.MetricSuccessRate, Value: w.traffic.SuccessRate, Unit: "ratio", Dimensions: cloneStringMap(dimensions)},
		platform.MetricPoint{At: w.now, Name: platform.MetricErrorRate, Value: 1 - w.traffic.SuccessRate, Unit: "ratio", Dimensions: cloneStringMap(dimensions)},
		platform.MetricPoint{At: w.now, Name: platform.MetricLatencyP95MS, Value: float64(w.traffic.LatencyP95MS), Unit: "ms", Dimensions: cloneStringMap(dimensions)},
	)
	if w.traffic.CostPerSuccess != nil {
		w.metrics = append(w.metrics, platform.MetricPoint{At: w.now, Name: platform.MetricCostPerSuccess, Value: *w.traffic.CostPerSuccess, Unit: "usd", Dimensions: cloneStringMap(dimensions)})
	}
	switch w.lastErrorType {
	case "authentication_failed":
		w.metrics = append(w.metrics, platform.MetricPoint{At: w.now, Name: platform.MetricAuthenticationErrorRate, Value: 1 - w.traffic.SuccessRate, Unit: "ratio", Dimensions: cloneStringMap(dimensions)})
	case "connection_refused":
		w.metrics = append(w.metrics, platform.MetricPoint{At: w.now, Name: platform.MetricConnectionErrorRate, Value: 1 - w.traffic.SuccessRate, Unit: "ratio", Dimensions: cloneStringMap(dimensions)})
	}
}

func (w *World) applyProtectionLocked(target, routeID string) {
	if routeID == "" {
		for id, route := range w.routes {
			if route.ProviderID == target {
				routeID = id
				break
			}
		}
	}
	route, ok := w.routes[routeID]
	if !ok {
		return
	}
	safeWeight, ok := asInt(w.controller.ProtectionPolicy["downweight_route_to"])
	if !ok || safeWeight < 0 {
		return
	}
	previousWeight := route.Weight
	route.Weight = safeWeight
	w.routes[routeID] = route
	w.affectedRouteID = routeID

	fallbackID := asString(w.controller.ProtectionPolicy["redistribute_to"])
	if fallback, exists := w.routes[fallbackID]; exists && fallback.Enabled {
		fallback.Weight += previousWeight - safeWeight
		w.routes[fallbackID] = fallback
	}
}

func (w *World) ingestTelemetryLocked(target string, telemetry map[string]any) {
	serviceID, toolName := w.initialServiceAndTool()
	defaultScope := platform.Scope{IncidentID: w.scenarioID, ServiceID: serviceID, ToolName: toolName, RouteID: w.affectedRouteID}
	if _, ok := w.providers[target]; ok {
		defaultScope.ProviderID = target
	}
	for _, item := range asMapSlice(telemetry["logs"]) {
		attributes := withoutKeys(item, "level", "component", "code", "message")
		w.logs = append(w.logs, platform.LogEntry{
			At: w.now, Level: asString(item["level"]), Component: asString(item["component"]),
			Code: asString(item["code"]), Message: asString(item["message"]), Scope: defaultScope,
			Attributes: attributes,
		})
	}
	for _, item := range asMapSlice(telemetry["traces"]) {
		scope := defaultScope
		if routeID := asString(item["route"]); routeID != "" {
			scope.RouteID = routeID
			if route, ok := w.routes[routeID]; ok {
				scope.ProviderID = route.ProviderID
			}
		}
		w.traces = append(w.traces, platform.TraceRecord{
			ID: asString(item["id"]), At: w.now, Scope: scope,
			TerminalSpan: asString(item["terminal_span"]), Status: asString(item["status"]),
			Attributes: withoutKeys(item, "id", "route", "terminal_span", "status"),
		})
	}
	if item := asMap(telemetry["change_record"]); len(item) > 0 {
		w.appendChangeLocked(item, defaultScope)
	}
	for _, item := range asMapSlice(telemetry["change_records"]) {
		w.appendChangeLocked(item, defaultScope)
	}
	w.applyCredentialTelemetryLocked(target, asMap(telemetry["credential_metadata"]))
	if metadata := asMap(telemetry["provider_metadata"]); len(metadata) > 0 {
		provider := w.providers[target]
		if endpoint := asString(metadata["endpoint"]); endpoint != "" {
			provider.Endpoint = endpoint
		}
		w.providers[target] = provider
	}
}

func (w *World) appendChangeLocked(item map[string]any, scope platform.Scope) {
	w.changes = append(w.changes, platform.ChangeRecord{
		ID: asString(item["id"]), At: w.now, Kind: asString(item["kind"]), Scope: scope,
		Actor: asString(item["actor"]), Attributes: withoutKeys(item, "id", "kind", "actor"),
	})
}

func (w *World) applyCredentialTelemetryLocked(providerID string, metadata map[string]any) {
	if len(metadata) == 0 {
		return
	}
	credential := w.credentials[providerID]
	credential.ProviderID = providerID
	if id := asString(metadata["credential_id"]); id != "" {
		credential.CredentialID = id
	}
	if status := asString(metadata["status"]); status != "" {
		credential.Status = status
	}
	w.credentials[providerID] = credential
}

func withoutKeys(source map[string]any, keys ...string) map[string]any {
	result := redactSensitiveMap(source)
	for _, key := range keys {
		delete(result, key)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func redactSensitiveMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	result := make(map[string]any, len(source))
	for key, value := range source {
		switch key {
		case "authorization", "api_key", "credential_value", "secret", "secret_reference":
			continue
		}
		switch typed := value.(type) {
		case map[string]any:
			result[key] = redactSensitiveMap(typed)
		case []any:
			items := make([]any, len(typed))
			for index, item := range typed {
				if mapped, ok := item.(map[string]any); ok {
					items[index] = redactSensitiveMap(mapped)
				} else {
					items[index] = cloneAny(item)
				}
			}
			result[key] = items
		default:
			result[key] = value
		}
	}
	return result
}

func asMap(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func asMapSlice(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		result := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if mapped := asMap(item); mapped != nil {
				result = append(result, mapped)
			}
		}
		return result
	default:
		return nil
	}
}

func asString(value any) string {
	result, _ := value.(string)
	return result
}

func asFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	default:
		return 0, false
	}
}

func asInt(value any) (int, bool) {
	result, ok := asFloat(value)
	return int(result), ok
}

func asInt64(value any) (int64, bool) {
	result, ok := asFloat(value)
	return int64(result), ok
}

func asBool(value any) (bool, bool) {
	result, ok := value.(bool)
	return result, ok
}
