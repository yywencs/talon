package simulator

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/scenario"
)

// recoverySession 保存一次逐级恢复的虚拟执行现场。
// protectedWeight 是恢复开始前的安全权重，任何异常都会原子退回该值。
type recoverySession struct {
	operationID     string
	routeID         string
	policy          scenario.RecoveryPolicy
	windowDuration  time.Duration
	stepProfiles    []probeWindowProfile
	stepIndex       int
	stepSampleCount int
	healthyWindows  int
	protectedWeight int
	baselineWeight  int
	dueAt           time.Time
	windows         []map[string]any
}

func (w *World) newRecoverySessionLocked(operationID, routeID string, behavior map[string]any) (*recoverySession, error) {
	windowDuration := w.tick
	if text := asString(behavior["window_duration"]); text != "" {
		parsed, err := time.ParseDuration(text)
		if err != nil || parsed <= 0 {
			return nil, fmt.Errorf("invalid recovery window_duration %q", text)
		}
		windowDuration = parsed
	}
	profilesData := asMapSlice(behavior["steps"])
	profiles := make([]probeWindowProfile, 0, len(w.controller.RecoveryPolicy.RecoverySteps))
	for index, fraction := range w.controller.RecoveryPolicy.RecoverySteps {
		data := map[string]any{}
		if len(profilesData) > 0 {
			profileIndex := index
			if profileIndex >= len(profilesData) {
				profileIndex = len(profilesData) - 1
			}
			data = profilesData[profileIndex]
		}
		profile, err := w.parseProbeWindowProfileLocked(data, behavior, fraction, windowDuration)
		if err != nil {
			return nil, fmt.Errorf("recovery step %d: %w", index+1, err)
		}
		profiles = append(profiles, profile)
	}
	if len(profiles) == 0 {
		return nil, fmt.Errorf("recovery policy has no steps")
	}
	return &recoverySession{
		operationID: operationID, routeID: routeID, policy: w.controller.RecoveryPolicy,
		windowDuration: windowDuration, stepProfiles: profiles,
		protectedWeight: w.routes[routeID].Weight, baselineWeight: w.routes[routeID].BaselineWeight,
		dueAt: w.now.Add(windowDuration),
	}, nil
}

func (w *World) processRecoveryWindowLocked(operationID string) {
	session, ok := w.recoveries[operationID]
	if !ok {
		return
	}
	profile := session.stepProfiles[session.stepIndex]
	fraction := session.policy.RecoverySteps[session.stepIndex]
	session.stepSampleCount += profile.sampleCount
	hardStop, hardStopReason := evaluateProbeHardStop(session.policy.HardStopWhen, profile)
	healthy, failedRequirements := evaluateProbeRequirements(session.policy.Require, profile)
	if healthy {
		session.healthyWindows++
	} else {
		session.healthyWindows = 0
	}
	if !healthy && !hardStop {
		hardStop = true
		hardStopReason = "recovery health requirements failed: " + strings.Join(failedRequirements, ",")
	}
	window := recoveryWindowEvidence(w.now.Add(-session.windowDuration), w.now, session, profile, fraction,
		healthy, hardStop, failedRequirements, hardStopReason)
	session.windows = append(session.windows, window)
	w.appendRecoveryMetricsLocked(session, profile, fraction)

	operation := w.operations[operationID]
	operation.Status = platform.OperationRunning
	operation.UpdatedAt = w.now
	operation.Message = "recovery health window completed"
	if hardStop {
		w.applyRouteWeightLocked(session.routeID, session.protectedWeight)
		operation.Status = platform.OperationSucceeded
		operation.Message = "recovery stopped and route returned to its protected weight"
		operation.Result = w.recoveryResultLocked(session, "hard_stop", hardStopReason)
		if len(profile.revealOnHardStop) > 0 {
			route := w.routes[session.routeID]
			w.ingestTelemetryLocked(route.ProviderID, profile.revealOnHardStop)
		}
		delete(w.recoveries, operationID)
		w.storeOperationLocked(operation)
		return
	}

	if session.stepSampleCount >= session.policy.MinRequestsPerStep &&
		session.healthyWindows >= session.policy.HealthyWindowsRequired {
		session.stepIndex++
		session.stepSampleCount = 0
		session.healthyWindows = 0
		if session.stepIndex == len(session.policy.RecoverySteps) {
			w.applyRouteWeightLocked(session.routeID, w.routes[session.routeID].BaselineWeight)
			operation.Status = platform.OperationSucceeded
			operation.Message = "all gradual recovery steps passed"
			operation.Result = w.recoveryResultLocked(session, "healthy", "")
			delete(w.recoveries, operationID)
			w.storeOperationLocked(operation)
			return
		}
		w.applyRecoveryWeightLocked(session, session.policy.RecoverySteps[session.stepIndex])
	}
	operation.Result = w.recoveryResultLocked(session, "running", "")
	session.dueAt = w.now.Add(session.windowDuration)
	w.storeOperationLocked(operation)
}

func (w *World) applyRecoveryWeightLocked(session *recoverySession, fraction float64) {
	route := w.routes[session.routeID]
	target := int(math.Round(float64(route.BaselineWeight) * fraction))
	if target < session.protectedWeight {
		target = session.protectedWeight
	}
	if target > route.BaselineWeight {
		target = route.BaselineWeight
	}
	w.applyRouteWeightLocked(session.routeID, target)
}

// applyRouteWeightLocked 调整受保护路由，并把差值交还给保护策略指定的备用路由。
func (w *World) applyRouteWeightLocked(routeID string, target int) {
	route := w.routes[routeID]
	delta := target - route.Weight
	route.Weight = target
	w.routes[routeID] = route
	fallbackID := asString(w.controller.ProtectionPolicy["redistribute_to"])
	if fallbackID == "" || fallbackID == routeID {
		return
	}
	if fallback, exists := w.routes[fallbackID]; exists {
		fallback.Weight -= delta
		w.routes[fallbackID] = fallback
	}
}

func recoveryWindowEvidence(from, to time.Time, session *recoverySession, profile probeWindowProfile, fraction float64,
	healthy, hardStop bool, failedRequirements []string, hardStopReason string,
) map[string]any {
	result := map[string]any{
		"from": from, "to": to, "step": session.stepIndex + 1, "traffic_fraction": fraction,
		"route_weight": sessionWeight(session, fraction), "sample_count": profile.sampleCount,
		"step_sample_count": session.stepSampleCount, "success_rate": profile.successRate,
		"error_rate": 1 - profile.successRate, "latency_p95_ms": profile.latencyP95MS,
		"telemetry_complete": profile.telemetryComplete, "error_types": append([]string(nil), profile.errorTypes...),
		"healthy": healthy, "hard_stop": hardStop, "failed_requirements": append([]string(nil), failedRequirements...),
	}
	if profile.costPerSuccess != nil {
		result["cost_per_success"] = *profile.costPerSuccess
	}
	if hardStopReason != "" {
		result["hard_stop_reason"] = hardStopReason
	}
	return result
}

func sessionWeight(session *recoverySession, fraction float64) int {
	target := int(math.Round(float64(session.baselineWeight) * fraction))
	if target < session.protectedWeight {
		return session.protectedWeight
	}
	return target
}

func (w *World) recoveryResultLocked(session *recoverySession, outcome, reason string) map[string]any {
	step := session.stepIndex + 1
	if step > len(session.policy.RecoverySteps) {
		step = len(session.policy.RecoverySteps)
	}
	route := w.routes[session.routeID]
	result := map[string]any{
		"outcome": outcome, "policy_id": session.policy.ID, "route_id": session.routeID,
		"current_step": step, "total_steps": len(session.policy.RecoverySteps), "route_weight": route.Weight,
		"baseline_weight": route.BaselineWeight, "protected_weight": session.protectedWeight,
		"healthy_windows": session.healthyWindows, "healthy_windows_required": session.policy.HealthyWindowsRequired,
		"step_sample_count": session.stepSampleCount, "windows": cloneAny(session.windows),
	}
	if reason != "" {
		result["reason"] = reason
	}
	return result
}

func (w *World) appendRecoveryMetricsLocked(session *recoverySession, profile probeWindowProfile, fraction float64) {
	route := w.routes[session.routeID]
	dimensions := map[string]string{
		"incident_id": w.scenarioID, "service_id": route.ServiceID, "tool_name": route.ToolName,
		"route_id": route.ID, "provider_id": route.ProviderID, "recovery_operation_id": session.operationID,
		"recovery_step": strconv.Itoa(session.stepIndex + 1), "traffic_fraction": strconv.FormatFloat(fraction, 'f', -1, 64),
	}
	w.metrics = append(w.metrics,
		platform.MetricPoint{At: w.now, Name: platform.MetricSuccessRate, Value: profile.successRate, Unit: "ratio", Dimensions: cloneStringMap(dimensions)},
		platform.MetricPoint{At: w.now, Name: platform.MetricErrorRate, Value: 1 - profile.successRate, Unit: "ratio", Dimensions: cloneStringMap(dimensions)},
		platform.MetricPoint{At: w.now, Name: platform.MetricLatencyP95MS, Value: profile.latencyP95MS, Unit: "ms", Dimensions: cloneStringMap(dimensions)},
	)
	if profile.costPerSuccess != nil {
		w.metrics = append(w.metrics, platform.MetricPoint{At: w.now, Name: platform.MetricCostPerSuccess, Value: *profile.costPerSuccess, Unit: "usd", Dimensions: cloneStringMap(dimensions)})
	}
}
