package simulator

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/scenario"
)

type probeSession struct {
	operationID     string
	routeID         string
	policy          scenario.RecoveryPolicy
	windowDuration  time.Duration
	stepProfiles    []probeWindowProfile
	stepIndex       int
	stepSampleCount int
	healthyWindows  int
	totalWindows    int
	dueAt           time.Time
	windows         []map[string]any
}

type probeWindowProfile struct {
	sampleCount       int
	successRate       float64
	latencyP95MS      float64
	costPerSuccess    *float64
	telemetryComplete bool
	errorTypes        []string
	revealOnHardStop  map[string]any
}

func (w *World) newProbeSessionLocked(operationID, routeID string, behavior map[string]any) (*probeSession, error) {
	attempt := behavior
	attempts := asMapSlice(behavior["attempts"])
	if len(attempts) > 0 {
		index := w.probeAttempt
		if index >= len(attempts) {
			index = len(attempts) - 1
		}
		attempt = attempts[index]
	}
	durationText := asString(attempt["window_duration"])
	if durationText == "" {
		durationText = asString(behavior["window_duration"])
	}
	windowDuration := w.tick
	if durationText != "" {
		parsed, err := time.ParseDuration(durationText)
		if err != nil || parsed <= 0 {
			return nil, fmt.Errorf("invalid probe window_duration %q", durationText)
		}
		windowDuration = parsed
	}

	profilesData := asMapSlice(attempt["steps"])
	if len(profilesData) == 0 {
		profilesData = asMapSlice(behavior["steps"])
	}
	profiles := make([]probeWindowProfile, 0, len(w.controller.RecoveryPolicy.ProbeSteps))
	for index := range w.controller.RecoveryPolicy.ProbeSteps {
		data := map[string]any{}
		if len(profilesData) > 0 {
			profileIndex := index
			if profileIndex >= len(profilesData) {
				profileIndex = len(profilesData) - 1
			}
			data = profilesData[profileIndex]
		}
		profile, err := w.parseProbeWindowProfileLocked(data, attempt, w.controller.RecoveryPolicy.ProbeSteps[index], windowDuration)
		if err != nil {
			return nil, fmt.Errorf("probe step %d: %w", index+1, err)
		}
		profiles = append(profiles, profile)
	}
	if len(profiles) == 0 {
		return nil, fmt.Errorf("probe policy has no steps")
	}
	return &probeSession{
		operationID: operationID, routeID: routeID, policy: w.controller.RecoveryPolicy,
		windowDuration: windowDuration, stepProfiles: profiles, dueAt: w.now.Add(windowDuration),
	}, nil
}

func (w *World) parseProbeWindowProfileLocked(data, legacy map[string]any, trafficFraction float64, windowDuration time.Duration) (probeWindowProfile, error) {
	estimatedSamples := int(math.Ceil(float64(w.traffic.RequestsPerMinute) * windowDuration.Minutes() * trafficFraction))
	if estimatedSamples < 1 {
		estimatedSamples = 1
	}
	profile := probeWindowProfile{
		sampleCount: estimatedSamples,
		successRate: w.traffic.SuccessRate, latencyP95MS: float64(w.traffic.LatencyP95MS),
		telemetryComplete: true,
	}
	if w.traffic.CostPerSuccess != nil {
		value := *w.traffic.CostPerSuccess
		profile.costPerSuccess = &value
	}
	if value, ok := asInt(data["sample_count"]); ok {
		profile.sampleCount = value
	}
	if value, ok := asFloat(data["success_rate"]); ok {
		profile.successRate = value
	}
	if value, ok := asFloat(data["latency_p95_ms"]); ok {
		profile.latencyP95MS = value
	}
	if value, ok := asFloat(data["cost_per_success"]); ok {
		profile.costPerSuccess = &value
	}
	if value, ok := asBool(data["telemetry_complete"]); ok {
		profile.telemetryComplete = value
	}
	profile.errorTypes = asStringSlice(data["error_types"])
	profile.revealOnHardStop = asMap(data["reveal_on_hard_stop"])

	// 兼容旧版 outcome 数据，但仍转换成异步指标窗口，由 Policy 做最终判断。
	legacyOutcome := asString(legacy["outcome"])
	if legacyOutcome == "hard_stop" && len(data) == 0 {
		threshold, ok := asFloat(w.controller.RecoveryPolicy.HardStopWhen["error_rate_gte"])
		if !ok {
			threshold = 0.05
		}
		profile.successRate = math.Max(0, 1-threshold)
		if reason := asString(legacy["reason"]); reason != "" {
			profile.errorTypes = []string{reason}
		}
		profile.revealOnHardStop = asMap(legacy["reveal_after_failure"])
	}
	if profile.sampleCount <= 0 {
		return probeWindowProfile{}, fmt.Errorf("sample_count must be positive")
	}
	if profile.successRate < 0 || profile.successRate > 1 {
		return probeWindowProfile{}, fmt.Errorf("success_rate must be between 0 and 1")
	}
	if profile.latencyP95MS <= 0 {
		return probeWindowProfile{}, fmt.Errorf("latency_p95_ms must be positive")
	}
	if profile.costPerSuccess != nil && *profile.costPerSuccess < 0 {
		return probeWindowProfile{}, fmt.Errorf("cost_per_success must not be negative")
	}
	return profile, nil
}

func (w *World) processProbeWindowLocked(operationID string) {
	session, ok := w.probes[operationID]
	if !ok {
		return
	}
	profile := session.stepProfiles[session.stepIndex]
	session.totalWindows++
	session.stepSampleCount += profile.sampleCount
	fraction := session.policy.ProbeSteps[session.stepIndex]
	hardStop, hardStopReason := evaluateProbeHardStop(session.policy.HardStopWhen, profile)
	healthy, failedRequirements := evaluateProbeRequirements(session.policy.Require, profile)
	if healthy {
		session.healthyWindows++
	} else {
		session.healthyWindows = 0
	}
	window := probeWindowEvidence(w.now.Add(-session.windowDuration), w.now, session, profile, fraction,
		healthy, hardStop, failedRequirements, hardStopReason)
	session.windows = append(session.windows, window)
	w.appendProbeMetricsLocked(session, profile, fraction)

	operation := w.operations[operationID]
	operation.Status = platform.OperationRunning
	operation.UpdatedAt = w.now
	operation.Message = "probe observation window completed"
	if hardStop {
		w.lastProbeOutcome = "hard_stop"
		operation.Status = platform.OperationSucceeded
		operation.Message = "probe stopped by a hard-stop condition"
		operation.Result = w.probeResultLocked(session, "hard_stop", hardStopReason)
		if len(profile.revealOnHardStop) > 0 {
			route := w.routes[session.routeID]
			w.ingestTelemetryLocked(route.ProviderID, profile.revealOnHardStop)
		}
		delete(w.probes, operationID)
		w.storeOperationLocked(operation)
		return
	}

	if session.stepSampleCount >= session.policy.MinRequestsPerStep &&
		session.healthyWindows >= session.policy.HealthyWindowsRequired {
		session.stepIndex++
		session.stepSampleCount = 0
		session.healthyWindows = 0
		if session.stepIndex == len(session.policy.ProbeSteps) {
			w.lastProbeOutcome = "healthy"
			operation.Status = platform.OperationSucceeded
			operation.Message = "all probe steps passed"
			operation.Result = w.probeResultLocked(session, "healthy", "")
			delete(w.probes, operationID)
			w.storeOperationLocked(operation)
			return
		}
	}
	operation.Result = w.probeResultLocked(session, "running", "")
	session.dueAt = w.now.Add(session.windowDuration)
	w.storeOperationLocked(operation)
}

func evaluateProbeRequirements(require map[string]any, profile probeWindowProfile) (bool, []string) {
	failed := make([]string, 0)
	for key, raw := range require {
		switch key {
		case "success_rate_gte":
			if threshold, ok := asFloat(raw); !ok || profile.successRate < threshold {
				failed = append(failed, key)
			}
		case "latency_p95_ms_lte":
			if threshold, ok := asFloat(raw); !ok || profile.latencyP95MS > threshold {
				failed = append(failed, key)
			}
		case "cost_per_success_lte":
			threshold, ok := asFloat(raw)
			if !ok || profile.costPerSuccess == nil || *profile.costPerSuccess > threshold {
				failed = append(failed, key)
			}
		case "telemetry_complete":
			required, ok := asBool(raw)
			if !ok || (required && !profile.telemetryComplete) {
				failed = append(failed, key)
			}
		default:
			failed = append(failed, "unsupported:"+key)
		}
	}
	sort.Strings(failed)
	return len(failed) == 0, failed
}

func evaluateProbeHardStop(conditions map[string]any, profile probeWindowProfile) (bool, string) {
	errorRate := 1 - profile.successRate
	errorSet := make(map[string]struct{}, len(profile.errorTypes))
	for _, value := range profile.errorTypes {
		errorSet[strings.ToLower(strings.TrimSpace(value))] = struct{}{}
	}
	keys := make([]string, 0, len(conditions))
	for key := range conditions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		raw := conditions[key]
		switch key {
		case "error_rate_gte":
			if threshold, ok := asFloat(raw); ok && errorRate >= threshold {
				return true, fmt.Sprintf("error_rate %.4f reached hard-stop threshold %.4f", errorRate, threshold)
			}
		case "new_error_type":
			if enabled, _ := asBool(raw); enabled && len(errorSet) > 0 {
				return true, "new error type observed: " + strings.Join(profile.errorTypes, ",")
			}
		default:
			if enabled, _ := asBool(raw); enabled && containsProbeErrorType(errorSet, key) {
				return true, "hard-stop error observed: " + key
			}
		}
	}
	return false, ""
}

func containsProbeErrorType(values map[string]struct{}, condition string) bool {
	condition = strings.ToLower(strings.TrimSpace(condition))
	for value := range values {
		if value == condition || strings.Contains(value, condition) || strings.Contains(condition, value) {
			return true
		}
	}
	return false
}

func probeWindowEvidence(from, to time.Time, session *probeSession, profile probeWindowProfile, fraction float64,
	healthy, hardStop bool, failedRequirements []string, hardStopReason string,
) map[string]any {
	result := map[string]any{
		"from": from, "to": to, "step": session.stepIndex + 1, "traffic_fraction": fraction,
		"sample_count": profile.sampleCount, "step_sample_count": session.stepSampleCount,
		"success_rate": profile.successRate, "error_rate": 1 - profile.successRate,
		"latency_p95_ms": profile.latencyP95MS, "telemetry_complete": profile.telemetryComplete,
		"error_types": append([]string(nil), profile.errorTypes...), "healthy": healthy, "hard_stop": hardStop,
		"failed_requirements": append([]string(nil), failedRequirements...),
	}
	if profile.costPerSuccess != nil {
		result["cost_per_success"] = *profile.costPerSuccess
	}
	if hardStopReason != "" {
		result["hard_stop_reason"] = hardStopReason
	}
	return result
}

func (w *World) probeResultLocked(session *probeSession, outcome, reason string) map[string]any {
	step := session.stepIndex + 1
	fraction := 0.0
	if session.stepIndex < len(session.policy.ProbeSteps) {
		fraction = session.policy.ProbeSteps[session.stepIndex]
	} else {
		step = len(session.policy.ProbeSteps)
		fraction = session.policy.ProbeSteps[len(session.policy.ProbeSteps)-1]
	}
	result := map[string]any{
		"outcome": outcome, "policy_id": session.policy.ID, "route_id": session.routeID,
		"current_step": step, "total_steps": len(session.policy.ProbeSteps),
		"traffic_fraction": fraction, "step_sample_count": session.stepSampleCount,
		"healthy_windows": session.healthyWindows, "healthy_windows_required": session.policy.HealthyWindowsRequired,
		"windows": cloneAny(session.windows),
	}
	if reason != "" {
		result["reason"] = reason
	}
	return result
}

func (w *World) appendProbeMetricsLocked(session *probeSession, profile probeWindowProfile, fraction float64) {
	route := w.routes[session.routeID]
	dimensions := map[string]string{
		"incident_id": w.scenarioID, "service_id": route.ServiceID, "tool_name": route.ToolName,
		"route_id": route.ID, "provider_id": route.ProviderID, "probe_operation_id": session.operationID,
		"probe_step": strconv.Itoa(session.stepIndex + 1), "traffic_fraction": strconv.FormatFloat(fraction, 'f', -1, 64),
	}
	w.metrics = append(w.metrics,
		platform.MetricPoint{At: w.now, Name: platform.MetricSuccessRate, Value: profile.successRate, Unit: "ratio", Dimensions: cloneStringMap(dimensions)},
		platform.MetricPoint{At: w.now, Name: platform.MetricErrorRate, Value: 1 - profile.successRate, Unit: "ratio", Dimensions: cloneStringMap(dimensions)},
		platform.MetricPoint{At: w.now, Name: platform.MetricLatencyP95MS, Value: profile.latencyP95MS, Unit: "ms", Dimensions: cloneStringMap(dimensions)},
	)
	if profile.costPerSuccess != nil {
		w.metrics = append(w.metrics, platform.MetricPoint{At: w.now, Name: platform.MetricCostPerSuccess, Value: *profile.costPerSuccess, Unit: "usd", Dimensions: cloneStringMap(dimensions)})
	}
	for _, errorType := range profile.errorTypes {
		switch errorType {
		case "authentication_failed", "authentication_error":
			w.metrics = append(w.metrics, platform.MetricPoint{At: w.now, Name: platform.MetricAuthenticationErrorRate, Value: 1 - profile.successRate, Unit: "ratio", Dimensions: cloneStringMap(dimensions)})
		case "connection_refused":
			w.metrics = append(w.metrics, platform.MetricPoint{At: w.now, Name: platform.MetricConnectionErrorRate, Value: 1 - profile.successRate, Unit: "ratio", Dimensions: cloneStringMap(dimensions)})
		}
	}
}

func asStringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := asString(item); text != "" {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}
