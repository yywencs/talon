package simulator

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/wen/opentalon/internal/platform"
)

func (s *Simulator) QueryMetrics(ctx context.Context, query platform.MetricQuery) (platform.MetricResult, error) {
	if err := contextError(ctx); err != nil {
		return platform.MetricResult{}, err
	}
	snapshot := s.Snapshot()
	if !snapshotMatchesIncident(snapshot, query.Scope) {
		return platform.MetricResult{Complete: true}, nil
	}
	if err := validateVisibleTimeRange(query.Range, snapshot.Now); err != nil {
		return platform.MetricResult{}, err
	}
	names := make(map[platform.MetricName]struct{}, len(query.Names))
	for _, name := range query.Names {
		names[name] = struct{}{}
	}
	result := platform.MetricResult{Complete: true}
	for _, point := range snapshot.Metrics {
		if !withinRange(point.At, query.Range) || !metricMatchesScope(point, query.Scope) {
			continue
		}
		if len(names) > 0 {
			if _, ok := names[point.Name]; !ok {
				continue
			}
		}
		result.Points = append(result.Points, point)
	}
	result.SampleCount = estimateSampleCount(snapshot, query.Range)
	return result, nil
}

func (s *Simulator) QueryLogs(ctx context.Context, query platform.LogQuery) ([]platform.LogEntry, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	snapshot := s.Snapshot()
	if err := validateVisibleTimeRange(query.Range, snapshot.Now); err != nil {
		return nil, err
	}
	if !snapshotMatchesIncident(snapshot, query.Scope) {
		return nil, nil
	}
	logs := snapshot.Logs
	result := make([]platform.LogEntry, 0, len(logs))
	for _, entry := range logs {
		if withinRange(entry.At, query.Range) && scopeMatches(entry.Scope, query.Scope) {
			result = append(result, entry)
			if query.Limit > 0 && len(result) >= query.Limit {
				break
			}
		}
	}
	return result, nil
}

func (s *Simulator) QueryTraces(ctx context.Context, query platform.TraceQuery) ([]platform.TraceRecord, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	snapshot := s.Snapshot()
	if err := validateVisibleTimeRange(query.Range, snapshot.Now); err != nil {
		return nil, err
	}
	if !snapshotMatchesIncident(snapshot, query.Scope) {
		return nil, nil
	}
	traces := snapshot.Traces
	result := make([]platform.TraceRecord, 0, len(traces))
	for _, entry := range traces {
		if withinRange(entry.At, query.Range) && scopeMatches(entry.Scope, query.Scope) {
			result = append(result, entry)
			if query.Limit > 0 && len(result) >= query.Limit {
				break
			}
		}
	}
	return result, nil
}

func (s *Simulator) GetServices(ctx context.Context, query platform.StateQuery) ([]platform.Service, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	snapshot := s.Snapshot()
	if !snapshotMatchesIncident(snapshot, query.Scope) {
		return nil, nil
	}
	ids := sortedKeys(snapshot.Services)
	result := make([]platform.Service, 0, len(ids))
	for _, id := range ids {
		service := snapshot.Services[id]
		if query.Scope.ServiceID != "" && query.Scope.ServiceID != service.ID {
			continue
		}
		if query.Scope.ToolName != "" {
			if _, ok := service.Tools[query.Scope.ToolName]; !ok {
				continue
			}
		}
		result = append(result, service)
	}
	return result, nil
}

func (s *Simulator) GetRoutes(ctx context.Context, query platform.StateQuery) ([]platform.Route, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	snapshot := s.Snapshot()
	if !snapshotMatchesIncident(snapshot, query.Scope) {
		return nil, nil
	}
	ids := sortedKeys(snapshot.Routes)
	result := make([]platform.Route, 0, len(ids))
	for _, id := range ids {
		route := snapshot.Routes[id]
		if routeMatchesScope(route, query.Scope) {
			result = append(result, route)
		}
	}
	return result, nil
}

func (s *Simulator) GetProviders(ctx context.Context, query platform.StateQuery) ([]platform.Provider, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	snapshot := s.Snapshot()
	if !snapshotMatchesIncident(snapshot, query.Scope) {
		return nil, nil
	}
	ids := sortedKeys(snapshot.Providers)
	result := make([]platform.Provider, 0, len(ids))
	for _, id := range ids {
		provider := snapshot.Providers[id]
		if query.Scope.ProviderID == "" || query.Scope.ProviderID == provider.ID {
			result = append(result, provider)
		}
	}
	return result, nil
}

func (s *Simulator) GetConfigVersions(ctx context.Context, query platform.StateQuery) ([]platform.ConfigVersion, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	snapshot := s.Snapshot()
	if !snapshotMatchesIncident(snapshot, query.Scope) {
		return nil, nil
	}
	ids := sortedKeys(snapshot.Configs)
	result := make([]platform.ConfigVersion, 0, len(ids))
	for _, id := range ids {
		result = append(result, snapshot.Configs[id])
	}
	return result, nil
}

func (s *Simulator) GetChangeRecords(ctx context.Context, query platform.ChangeQuery) ([]platform.ChangeRecord, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	snapshot := s.Snapshot()
	if err := validateVisibleTimeRange(query.Range, snapshot.Now); err != nil {
		return nil, err
	}
	if !snapshotMatchesIncident(snapshot, query.Scope) {
		return nil, nil
	}
	changes := snapshot.Changes
	result := make([]platform.ChangeRecord, 0, len(changes))
	for _, entry := range changes {
		if withinRange(entry.At, query.Range) && scopeMatches(entry.Scope, query.Scope) {
			result = append(result, entry)
		}
	}
	return result, nil
}

func (s *Simulator) GetCredentialMetadata(ctx context.Context, query platform.StateQuery) ([]platform.CredentialMetadata, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	snapshot := s.Snapshot()
	if !snapshotMatchesIncident(snapshot, query.Scope) {
		return nil, nil
	}
	ids := sortedKeys(snapshot.Credentials)
	result := make([]platform.CredentialMetadata, 0, len(ids))
	for _, id := range ids {
		credential := snapshot.Credentials[id]
		if query.Scope.ProviderID == "" || query.Scope.ProviderID == credential.ProviderID {
			result = append(result, credential)
		}
	}
	return result, nil
}

func (s *Simulator) GetConnectionMetadata(ctx context.Context, query platform.StateQuery) ([]platform.ConnectionMetadata, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	snapshot := s.Snapshot()
	if !snapshotMatchesIncident(snapshot, query.Scope) {
		return nil, nil
	}
	ids := sortedKeys(snapshot.Connections)
	result := make([]platform.ConnectionMetadata, 0, len(ids))
	for _, id := range ids {
		connection := snapshot.Connections[id]
		if query.Scope.ProviderID == "" || query.Scope.ProviderID == connection.ProviderID {
			result = append(result, connection)
		}
	}
	return result, nil
}

func (s *Simulator) GetTasks(ctx context.Context, query platform.TaskQuery) ([]platform.ManagedTask, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	statuses := make(map[platform.TaskStatus]struct{}, len(query.Statuses))
	for _, status := range query.Statuses {
		statuses[status] = struct{}{}
	}
	snapshot := s.Snapshot()
	if !snapshotMatchesIncident(snapshot, query.Scope) {
		return nil, nil
	}
	ids := sortedKeys(snapshot.Tasks)
	result := make([]platform.ManagedTask, 0, len(ids))
	for _, id := range ids {
		task := snapshot.Tasks[id]
		if query.Scope.ProviderID != "" && query.Scope.ProviderID != task.ProviderID {
			continue
		}
		if len(statuses) > 0 {
			if _, ok := statuses[task.Status]; !ok {
				continue
			}
		}
		result = append(result, task)
	}
	return result, nil
}

// GetRemediationCapabilities 返回当前 Agent 有权调用的修复函数目录。
func (s *Simulator) GetRemediationCapabilities(ctx context.Context, query platform.StateQuery) ([]platform.RemediationCapability, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	snapshot := s.Snapshot()
	if !snapshotMatchesIncident(snapshot, query.Scope) {
		return nil, nil
	}
	w, err := s.mutableWorld()
	if err != nil {
		return nil, err
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	result := make([]platform.RemediationCapability, 0, len(w.remediationTools))
	for _, definition := range w.remediationTools {
		if definition.AgentAuthorized != nil && !*definition.AgentAuthorized {
			continue
		}
		result = append(result, platform.RemediationCapability{
			Name:             definition.Name,
			Description:      definition.Description,
			Risk:             definition.Risk,
			RequiresApproval: definition.RequiresApproval,
			Arguments:        append([]string(nil), definition.Arguments...),
			Preconditions:    cloneAnyMap(definition.Preconditions),
		})
	}
	return result, nil
}

// GetRecoveryPolicies 返回 Controller 允许当前 Incident 引用的恢复策略。
// 策略只读；实际探测比例和恢复权重始终由 Controller 执行。
func (s *Simulator) GetRecoveryPolicies(ctx context.Context, query platform.StateQuery) ([]platform.RecoveryPolicy, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	snapshot := s.Snapshot()
	if !snapshotMatchesIncident(snapshot, query.Scope) {
		return nil, nil
	}
	w, err := s.mutableWorld()
	if err != nil {
		return nil, err
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	policy := w.controller.RecoveryPolicy
	return []platform.RecoveryPolicy{{
		ID:                     policy.ID,
		ProbeSteps:             append([]float64(nil), policy.ProbeSteps...),
		RecoverySteps:          append([]float64(nil), policy.RecoverySteps...),
		StepMode:               policy.StepMode,
		MinRequestsPerStep:     policy.MinRequestsPerStep,
		HealthyWindowsRequired: policy.HealthyWindowsRequired,
		Require:                cloneAnyMap(policy.Require),
		HardStopWhen:           cloneAnyMap(policy.HardStopWhen),
	}}, nil
}

func validateTimeRange(value platform.TimeRange) error {
	if value.From.IsZero() || value.To.IsZero() {
		return nil
	}
	if value.From.After(value.To) {
		return fmt.Errorf("time range from must not be after to")
	}
	return nil
}

func validateVisibleTimeRange(value platform.TimeRange, virtualTime time.Time) error {
	if err := validateTimeRange(value); err != nil {
		return err
	}
	if !value.From.IsZero() && !virtualTime.IsZero() && value.From.After(virtualTime) {
		return fmt.Errorf("time range from %s is after current Incident virtual time %s; retry with from at or before virtual time, or omit from", value.From.Format(time.RFC3339), virtualTime.Format(time.RFC3339))
	}
	return nil
}

func withinRange(at time.Time, value platform.TimeRange) bool {
	if !value.From.IsZero() && at.Before(value.From) {
		return false
	}
	if !value.To.IsZero() && at.After(value.To) {
		return false
	}
	return true
}

func scopeMatches(actual, requested platform.Scope) bool {
	return (requested.IncidentID == "" || requested.IncidentID == actual.IncidentID) &&
		(requested.ServiceID == "" || requested.ServiceID == actual.ServiceID) &&
		(requested.ToolName == "" || requested.ToolName == actual.ToolName) &&
		(requested.RouteID == "" || requested.RouteID == actual.RouteID) &&
		(requested.ProviderID == "" || requested.ProviderID == actual.ProviderID)
}

func metricMatchesScope(point platform.MetricPoint, requested platform.Scope) bool {
	return (requested.IncidentID == "" || requested.IncidentID == point.Dimensions["incident_id"]) &&
		(requested.ServiceID == "" || requested.ServiceID == point.Dimensions["service_id"]) &&
		(requested.ToolName == "" || requested.ToolName == point.Dimensions["tool_name"]) &&
		(requested.RouteID == "" || requested.RouteID == point.Dimensions["route_id"]) &&
		(requested.ProviderID == "" || requested.ProviderID == point.Dimensions["provider_id"])
}

func snapshotMatchesIncident(snapshot Snapshot, scope platform.Scope) bool {
	return scope.IncidentID == "" || scope.IncidentID == snapshot.ScenarioID
}

func routeMatchesScope(route platform.Route, requested platform.Scope) bool {
	return (requested.ServiceID == "" || requested.ServiceID == route.ServiceID) &&
		(requested.ToolName == "" || requested.ToolName == route.ToolName) &&
		(requested.RouteID == "" || requested.RouteID == route.ID) &&
		(requested.ProviderID == "" || requested.ProviderID == route.ProviderID)
}

func estimateSampleCount(snapshot Snapshot, value platform.TimeRange) int {
	duration := snapshot.Tick
	if !value.From.IsZero() && !value.To.IsZero() {
		duration = value.To.Sub(value.From)
	}
	minutes := int(duration / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	return snapshot.Traffic.RequestsPerMinute * minutes
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
