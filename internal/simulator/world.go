// Package simulator 提供 ToolOpsPlatform 的有状态虚拟运行环境。
package simulator

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/scenario"
)

// World 保存 Simulator 当前时刻的完整内部状态。
// Timeline 和动作行为包含隐藏信息，因此只能由 Simulator 内部使用。
type World struct {
	mu sync.RWMutex

	scenarioID string
	startAt    time.Time
	now        time.Time
	tick       time.Duration
	endAt      time.Time

	services          map[string]platform.Service
	providers         map[string]platform.Provider
	routes            map[string]platform.Route
	configs           map[string]platform.ConfigVersion
	credentials       map[string]platform.CredentialMetadata
	connections       map[string]platform.ConnectionMetadata
	tasks             map[string]platform.ManagedTask
	traffic           platform.TrafficProfile
	metrics           []platform.MetricPoint
	logs              []platform.LogEntry
	traces            []platform.TraceRecord
	changes           []platform.ChangeRecord
	operations        map[string]platform.Operation
	idempotency       map[string]string
	pending           map[string]scheduledOperation
	probes            map[string]*probeSession
	recoveries        map[string]*recoverySession
	operationSequence int
	probeAttempt      int
	lastProbeOutcome  string
	affectedRouteID   string
	lastErrorType     string

	controller        scenario.Controller
	agentPolicy       scenario.AgentPolicy
	timeline          []scenario.TimelineEvent
	nextTimelineEvent int
	remediationTools  []scenario.ToolDefinition
	probeTool         scenario.ToolDefinition
	escalationTool    scenario.ToolDefinition
	actionBehavior    map[string]map[string]any
}

// Snapshot 是不包含 Timeline、内部故障原因和动作规则的 World 只读副本。
type Snapshot struct {
	ScenarioID  string
	Now         time.Time
	Tick        time.Duration
	EndAt       time.Time
	Services    map[string]platform.Service
	Providers   map[string]platform.Provider
	Routes      map[string]platform.Route
	Configs     map[string]platform.ConfigVersion
	Credentials map[string]platform.CredentialMetadata
	Connections map[string]platform.ConnectionMetadata
	Tasks       map[string]platform.ManagedTask
	Traffic     platform.TrafficProfile
	Metrics     []platform.MetricPoint
	Logs        []platform.LogEntry
	Traces      []platform.TraceRecord
	Changes     []platform.ChangeRecord
	Operations  map[string]platform.Operation
}

// NewWorld 使用已经通过场景加载器校验的 Scenario 初始化虚拟世界。
func NewWorld(document scenario.Scenario) (*World, error) {
	startAt, err := time.Parse(time.RFC3339, document.Clock.StartAt)
	if err != nil {
		return nil, fmt.Errorf("parse world start time: %w", err)
	}
	tick, err := time.ParseDuration(document.Clock.Tick)
	if err != nil || tick <= 0 {
		return nil, fmt.Errorf("world tick %q must be a positive duration", document.Clock.Tick)
	}
	endAfter, err := time.ParseDuration(document.Clock.EndAfter)
	if err != nil || endAfter <= 0 {
		return nil, fmt.Errorf("world end_after %q must be a positive duration", document.Clock.EndAfter)
	}

	world := &World{
		scenarioID:       document.Metadata.ID,
		startAt:          startAt,
		now:              startAt,
		tick:             tick,
		endAt:            startAt.Add(endAfter),
		services:         make(map[string]platform.Service),
		providers:        make(map[string]platform.Provider),
		routes:           make(map[string]platform.Route),
		configs:          make(map[string]platform.ConfigVersion),
		credentials:      make(map[string]platform.CredentialMetadata),
		connections:      make(map[string]platform.ConnectionMetadata),
		tasks:            make(map[string]platform.ManagedTask),
		operations:       make(map[string]platform.Operation),
		idempotency:      make(map[string]string),
		pending:          make(map[string]scheduledOperation),
		probes:           make(map[string]*probeSession),
		recoveries:       make(map[string]*recoverySession),
		controller:       document.Controller,
		agentPolicy:      document.AgentPolicy,
		timeline:         append([]scenario.TimelineEvent(nil), document.Timeline...),
		remediationTools: append([]scenario.ToolDefinition(nil), document.RemediationTools...),
		probeTool:        document.ProbeTool,
		escalationTool:   document.EscalationTool,
		actionBehavior:   cloneNestedMap(document.ActionBehavior),
	}
	if strings.TrimSpace(world.scenarioID) == "" {
		return nil, fmt.Errorf("scenario ID is required")
	}
	if err := world.initializeProviders(document.InitialState.Providers); err != nil {
		return nil, err
	}
	if err := world.initializeService(document.InitialState.Service); err != nil {
		return nil, err
	}
	if err := world.initializeOptionalState(document.InitialState); err != nil {
		return nil, err
	}
	world.initializeTraffic(document.InitialState.Traffic)
	return world, nil
}

func (w *World) initializeProviders(initial []scenario.InitialProvider) error {
	for _, item := range initial {
		if _, exists := w.providers[item.ID]; exists {
			return fmt.Errorf("duplicate provider %q", item.ID)
		}
		health := platform.ProviderHealth(item.Health)
		switch health {
		case platform.ProviderHealthy, platform.ProviderDegraded, platform.ProviderUnavailable:
		default:
			return fmt.Errorf("provider %q has invalid health %q", item.ID, item.Health)
		}
		w.providers[item.ID] = platform.Provider{
			ID:               item.ID,
			Health:           health,
			Endpoint:         item.Endpoint,
			SchemaCompatible: cloneBoolPointer(item.SchemaCompatible),
		}
	}
	return nil
}

func (w *World) initializeService(initial scenario.InitialService) error {
	if len(w.services) != 0 {
		return fmt.Errorf("world currently supports one initial service")
	}
	routeIDs := make([]string, 0, len(initial.Routes))
	totalWeight := 0
	for _, item := range initial.Routes {
		if _, exists := w.routes[item.ID]; exists {
			return fmt.Errorf("duplicate route %q", item.ID)
		}
		if _, exists := w.providers[item.Provider]; !exists {
			return fmt.Errorf("route %q references unknown provider %q", item.ID, item.Provider)
		}
		w.routes[item.ID] = platform.Route{
			ID:                item.ID,
			ServiceID:         initial.ID,
			ToolName:          initial.Tool,
			ProviderID:        item.Provider,
			BaselineWeight:    item.Weight,
			Weight:            item.Weight,
			Enabled:           item.Enabled,
			UnavailableReason: item.UnavailableReason,
		}
		routeIDs = append(routeIDs, item.ID)
		totalWeight += item.Weight
	}
	if totalWeight != 100 {
		return fmt.Errorf("initial route weights must total 100, got %d", totalWeight)
	}
	costLimit := cloneFloatPointer(initial.SLO.CostPerSuccessMax)
	w.services[initial.ID] = platform.Service{
		ID: initial.ID,
		Tools: map[string]platform.Tool{
			initial.Tool: {
				Name: initial.Tool,
				SLO: platform.SLO{
					SuccessRateMin:    initial.SLO.SuccessRateMin,
					LatencyP95MSMax:   initial.SLO.LatencyP95MSMax,
					CostPerSuccessMax: costLimit,
				},
				RouteIDs: routeIDs,
			},
		},
	}
	return nil
}

func (w *World) initializeOptionalState(initial scenario.InitialState) error {
	if initial.Config != nil {
		for _, version := range initial.Config.KnownHealthyVersions {
			w.configs[version] = platform.ConfigVersion{ID: version, KnownHealthy: true}
		}
		current := w.configs[initial.Config.CurrentVersion]
		current.ID = initial.Config.CurrentVersion
		current.Active = true
		w.configs[current.ID] = current
	}
	if initial.CredentialMetadata != nil {
		item := initial.CredentialMetadata
		if item.SecretVisible {
			return fmt.Errorf("credential %q must not expose secret material", item.CredentialID)
		}
		if _, exists := w.providers[item.Provider]; !exists {
			return fmt.Errorf("credential %q references unknown provider %q", item.CredentialID, item.Provider)
		}
		w.credentials[item.Provider] = platform.CredentialMetadata{
			ProviderID:   item.Provider,
			CredentialID: item.CredentialID,
			Status:       item.Status,
			ManagedBy:    item.ManagedBy,
		}
	}
	for providerID, item := range initial.Connection {
		if _, exists := w.providers[providerID]; !exists {
			return fmt.Errorf("connection metadata references unknown provider %q", providerID)
		}
		w.connections[providerID] = platform.ConnectionMetadata{
			ProviderID:              providerID,
			PoolGeneration:          item.PoolGeneration,
			ResolverCacheGeneration: item.ResolverCacheGeneration,
			ResolvedIP:              item.ResolvedIP,
		}
	}
	for _, item := range initial.Tasks {
		if _, exists := w.tasks[item.ID]; exists {
			return fmt.Errorf("duplicate task %q", item.ID)
		}
		if item.ProviderID != "" {
			if _, exists := w.providers[item.ProviderID]; !exists {
				return fmt.Errorf("task %q references unknown provider %q", item.ID, item.ProviderID)
			}
		}
		status := platform.TaskStatus(item.Status)
		switch status {
		case platform.TaskCreated, platform.TaskProcessing, platform.TaskFinished, platform.TaskFailed, platform.TaskCanceled:
		default:
			return fmt.Errorf("task %q has invalid status %q", item.ID, item.Status)
		}
		w.tasks[item.ID] = platform.ManagedTask{
			ID: item.ID, Type: item.Type, Name: item.Name, Status: status,
			ProviderID: item.ProviderID, Attempts: item.Attempts, Idempotent: item.Idempotent,
			CreatedAt: w.now, UpdatedAt: w.now, LastError: item.LastError,
			Attributes: cloneAnyMap(item.Attributes),
		}
	}
	return nil
}

func (w *World) initializeTraffic(initial scenario.InitialTraffic) {
	w.traffic = platform.TrafficProfile{
		RequestsPerMinute: initial.RequestsPerMinute,
		SuccessRate:       initial.SuccessRate,
		LatencyP95MS:      initial.LatencyP95MS,
		CostPerSuccess:    cloneFloatPointer(initial.CostPerSuccess),
	}
	serviceID, toolName := w.initialServiceAndTool()
	dimensions := map[string]string{"incident_id": w.scenarioID, "service_id": serviceID, "tool_name": toolName}
	w.metrics = []platform.MetricPoint{
		{At: w.now, Name: platform.MetricSuccessRate, Value: initial.SuccessRate, Unit: "ratio", Dimensions: cloneStringMap(dimensions)},
		{At: w.now, Name: platform.MetricErrorRate, Value: 1 - initial.SuccessRate, Unit: "ratio", Dimensions: cloneStringMap(dimensions)},
		{At: w.now, Name: platform.MetricLatencyP95MS, Value: float64(initial.LatencyP95MS), Unit: "ms", Dimensions: cloneStringMap(dimensions)},
	}
	if initial.CostPerSuccess != nil {
		w.metrics = append(w.metrics, platform.MetricPoint{
			At: w.now, Name: platform.MetricCostPerSuccess, Value: *initial.CostPerSuccess,
			Unit: "usd", Dimensions: cloneStringMap(dimensions),
		})
	}
}

func (w *World) initialServiceAndTool() (string, string) {
	for serviceID, service := range w.services {
		for toolName := range service.Tools {
			return serviceID, toolName
		}
	}
	return "", ""
}

// Snapshot 返回与内部可变状态隔离的深拷贝。
func (w *World) Snapshot() Snapshot {
	if w == nil {
		return Snapshot{}
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return Snapshot{
		ScenarioID:  w.scenarioID,
		Now:         w.now,
		Tick:        w.tick,
		EndAt:       w.endAt,
		Services:    cloneServices(w.services),
		Providers:   cloneProviders(w.providers),
		Routes:      cloneMap(w.routes),
		Configs:     cloneConfigs(w.configs),
		Credentials: cloneMap(w.credentials),
		Connections: cloneConnections(w.connections),
		Tasks:       cloneTasks(w.tasks),
		Traffic:     cloneTraffic(w.traffic),
		Metrics:     cloneMetrics(w.metrics),
		Logs:        cloneLogs(w.logs),
		Traces:      cloneTraces(w.traces),
		Changes:     cloneChanges(w.changes),
		Operations:  cloneOperations(w.operations),
	}
}

func cloneServices(source map[string]platform.Service) map[string]platform.Service {
	result := make(map[string]platform.Service, len(source))
	for id, service := range source {
		tools := make(map[string]platform.Tool, len(service.Tools))
		for name, tool := range service.Tools {
			tool.RouteIDs = append([]string(nil), tool.RouteIDs...)
			tool.SLO.CostPerSuccessMax = cloneFloatPointer(tool.SLO.CostPerSuccessMax)
			tools[name] = tool
		}
		service.Tools = tools
		result[id] = service
	}
	return result
}

func cloneProviders(source map[string]platform.Provider) map[string]platform.Provider {
	result := make(map[string]platform.Provider, len(source))
	for id, provider := range source {
		provider.SchemaCompatible = cloneBoolPointer(provider.SchemaCompatible)
		result[id] = provider
	}
	return result
}

func cloneConfigs(source map[string]platform.ConfigVersion) map[string]platform.ConfigVersion {
	result := make(map[string]platform.ConfigVersion, len(source))
	for id, config := range source {
		config.Attributes = cloneAnyMap(config.Attributes)
		result[id] = config
	}
	return result
}

func cloneConnections(source map[string]platform.ConnectionMetadata) map[string]platform.ConnectionMetadata {
	result := make(map[string]platform.ConnectionMetadata, len(source))
	for id, connection := range source {
		connection.LastPingAt = cloneTimePointer(connection.LastPingAt)
		connection.Attributes = cloneAnyMap(connection.Attributes)
		result[id] = connection
	}
	return result
}

func cloneTasks(source map[string]platform.ManagedTask) map[string]platform.ManagedTask {
	result := make(map[string]platform.ManagedTask, len(source))
	for id, task := range source {
		task.Attributes = cloneAnyMap(task.Attributes)
		result[id] = task
	}
	return result
}

func cloneTraffic(source platform.TrafficProfile) platform.TrafficProfile {
	source.CostPerSuccess = cloneFloatPointer(source.CostPerSuccess)
	return source
}

func cloneMetrics(source []platform.MetricPoint) []platform.MetricPoint {
	result := append([]platform.MetricPoint(nil), source...)
	for index := range result {
		result[index].Dimensions = cloneStringMap(result[index].Dimensions)
	}
	return result
}

func cloneLogs(source []platform.LogEntry) []platform.LogEntry {
	result := append([]platform.LogEntry(nil), source...)
	for index := range result {
		result[index].Attributes = cloneAnyMap(result[index].Attributes)
	}
	return result
}

func cloneTraces(source []platform.TraceRecord) []platform.TraceRecord {
	result := append([]platform.TraceRecord(nil), source...)
	for index := range result {
		result[index].Attributes = cloneAnyMap(result[index].Attributes)
	}
	return result
}

func cloneChanges(source []platform.ChangeRecord) []platform.ChangeRecord {
	result := append([]platform.ChangeRecord(nil), source...)
	for index := range result {
		result[index].Attributes = cloneAnyMap(result[index].Attributes)
	}
	return result
}

func cloneOperations(source map[string]platform.Operation) map[string]platform.Operation {
	result := make(map[string]platform.Operation, len(source))
	for id, operation := range source {
		operation.Result = cloneAnyMap(operation.Result)
		result[id] = operation
	}
	return result
}

func cloneNestedMap(source map[string]map[string]any) map[string]map[string]any {
	result := make(map[string]map[string]any, len(source))
	for key, value := range source {
		result[key] = cloneAnyMap(value)
	}
	return result
}

func cloneAnyMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = cloneAny(value)
	}
	return result
}

func cloneAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneAnyMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index := range typed {
			result[index] = cloneAny(typed[index])
		}
		return result
	case []map[string]any:
		result := make([]map[string]any, len(typed))
		for index := range typed {
			result[index] = cloneAnyMap(typed[index])
		}
		return result
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneFloatPointer(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneMap[K comparable, V any](source map[K]V) map[K]V {
	result := make(map[K]V, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
