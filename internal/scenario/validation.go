package scenario

import (
	"fmt"
	"strings"
	"time"
)

var allowedDifficulties = map[string]struct{}{
	"basic":        {},
	"intermediate": {},
	"adversarial":  {},
}

func validateScenario(document Scenario) error {
	if document.SchemaVersion != ScenarioSchemaV1_1 {
		return fmt.Errorf("unsupported scenario schema %q", document.SchemaVersion)
	}
	if err := validateMetadata(document.Metadata); err != nil {
		return err
	}
	endAfter, err := validateClock(document.Clock)
	if err != nil {
		return err
	}
	if err := validateInitialState(document.InitialState); err != nil {
		return err
	}
	if err := validateController(document.Controller); err != nil {
		return err
	}
	if err := validateAgentPolicy(document.AgentPolicy); err != nil {
		return err
	}
	if err := validateTimeline(document.Timeline, endAfter); err != nil {
		return err
	}
	if err := validateObservation(document.Observation); err != nil {
		return err
	}
	if err := validateTools(document.RemediationTools, document.ProbeTool, document.EscalationTool); err != nil {
		return err
	}
	if len(document.ActionBehavior) == 0 {
		return fmt.Errorf("action_behavior is required")
	}
	return nil
}

func validateInitialState(state InitialState) error {
	if strings.TrimSpace(state.Service.ID) == "" {
		return fmt.Errorf("initial_state.service.id is required")
	}
	if strings.TrimSpace(state.Service.Tool) == "" {
		return fmt.Errorf("initial_state.service.tool is required")
	}
	if state.Service.SLO.SuccessRateMin <= 0 || state.Service.SLO.SuccessRateMin > 1 {
		return fmt.Errorf("initial_state.service.slo.success_rate_min must be within (0, 1]")
	}
	if state.Service.SLO.LatencyP95MSMax <= 0 {
		return fmt.Errorf("initial_state.service.slo.latency_p95_ms_max must be positive")
	}
	if state.Service.SLO.CostPerSuccessMax != nil && *state.Service.SLO.CostPerSuccessMax <= 0 {
		return fmt.Errorf("initial_state.service.slo.cost_per_success_max must be positive")
	}
	if len(state.Service.Routes) == 0 {
		return fmt.Errorf("initial_state.service.routes is required")
	}
	routeIDs := make([]string, 0, len(state.Service.Routes))
	for index, route := range state.Service.Routes {
		if strings.TrimSpace(route.ID) == "" || strings.TrimSpace(route.Provider) == "" {
			return fmt.Errorf("initial_state.service.routes[%d] requires id and provider", index)
		}
		if route.Weight < 0 || route.Weight > 100 {
			return fmt.Errorf("initial_state.service.routes[%d].weight must be within [0, 100]", index)
		}
		routeIDs = append(routeIDs, route.ID)
	}
	if err := validateUniqueStrings("initial_state.service.routes.id", routeIDs); err != nil {
		return err
	}
	if len(state.Providers) == 0 {
		return fmt.Errorf("initial_state.providers is required")
	}
	providerIDs := make([]string, 0, len(state.Providers))
	for index, provider := range state.Providers {
		if strings.TrimSpace(provider.ID) == "" || strings.TrimSpace(provider.Health) == "" {
			return fmt.Errorf("initial_state.providers[%d] requires id and health", index)
		}
		providerIDs = append(providerIDs, provider.ID)
	}
	if err := validateUniqueStrings("initial_state.providers.id", providerIDs); err != nil {
		return err
	}
	providers := make(map[string]struct{}, len(providerIDs))
	for _, id := range providerIDs {
		providers[id] = struct{}{}
	}
	for index, route := range state.Service.Routes {
		if _, ok := providers[route.Provider]; !ok {
			return fmt.Errorf("initial_state.service.routes[%d] references unknown provider %q", index, route.Provider)
		}
	}
	if state.CredentialMetadata != nil {
		if _, ok := providers[state.CredentialMetadata.Provider]; !ok {
			return fmt.Errorf("initial_state.credential_metadata references unknown provider %q", state.CredentialMetadata.Provider)
		}
		if state.CredentialMetadata.SecretVisible {
			return fmt.Errorf("initial_state.credential_metadata must not expose secret material")
		}
	}
	for providerID := range state.Connection {
		if _, ok := providers[providerID]; !ok {
			return fmt.Errorf("initial_state.connection references unknown provider %q", providerID)
		}
	}
	allowedTaskStatuses := map[string]struct{}{
		"created": {}, "processing": {}, "finished": {}, "failed": {}, "canceled": {},
	}
	taskIDs := make([]string, 0, len(state.Tasks))
	for index, task := range state.Tasks {
		if strings.TrimSpace(task.ID) == "" || strings.TrimSpace(task.Type) == "" || strings.TrimSpace(task.Name) == "" {
			return fmt.Errorf("initial_state.tasks[%d] requires id, type and name", index)
		}
		if _, ok := allowedTaskStatuses[task.Status]; !ok {
			return fmt.Errorf("initial_state.tasks[%d].status %q is invalid", index, task.Status)
		}
		if task.Attempts < 0 {
			return fmt.Errorf("initial_state.tasks[%d].attempts must not be negative", index)
		}
		if task.ProviderID != "" {
			if _, ok := providers[task.ProviderID]; !ok {
				return fmt.Errorf("initial_state.tasks[%d] references unknown provider %q", index, task.ProviderID)
			}
		}
		taskIDs = append(taskIDs, task.ID)
	}
	if err := validateUniqueStrings("initial_state.tasks.id", taskIDs); err != nil {
		return err
	}
	if state.Traffic.RequestsPerMinute <= 0 {
		return fmt.Errorf("initial_state.traffic.requests_per_minute must be positive")
	}
	if state.Traffic.SuccessRate < 0 || state.Traffic.SuccessRate > 1 {
		return fmt.Errorf("initial_state.traffic.success_rate must be within [0, 1]")
	}
	if state.Traffic.LatencyP95MS <= 0 {
		return fmt.Errorf("initial_state.traffic.latency_p95_ms must be positive")
	}
	if state.Traffic.CostPerSuccess != nil && *state.Traffic.CostPerSuccess < 0 {
		return fmt.Errorf("initial_state.traffic.cost_per_success must not be negative")
	}
	return nil
}

func validateMetadata(metadata Metadata) error {
	if strings.TrimSpace(metadata.ID) == "" {
		return fmt.Errorf("metadata.id is required")
	}
	if strings.TrimSpace(metadata.Title) == "" {
		return fmt.Errorf("metadata.title is required")
	}
	if strings.TrimSpace(metadata.Category) == "" {
		return fmt.Errorf("metadata.category is required")
	}
	if _, ok := allowedDifficulties[metadata.Difficulty]; !ok {
		return fmt.Errorf("metadata.difficulty %q is invalid", metadata.Difficulty)
	}
	return nil
}

func validateClock(clock Clock) (time.Duration, error) {
	if _, err := time.Parse(time.RFC3339, clock.StartAt); err != nil {
		return 0, fmt.Errorf("clock.start_at must be RFC3339: %w", err)
	}
	if _, err := positiveDuration("clock.tick", clock.Tick); err != nil {
		return 0, err
	}
	endAfter, err := positiveDuration("clock.end_after", clock.EndAfter)
	if err != nil {
		return 0, err
	}
	return endAfter, nil
}

func validateController(controller Controller) error {
	if _, err := positiveDuration("controller.detection_policy.window", controller.DetectionPolicy.Window); err != nil {
		return err
	}
	if controller.DetectionPolicy.MinRequests <= 0 {
		return fmt.Errorf("controller.detection_policy.min_requests must be positive")
	}
	if len(controller.DetectionPolicy.TriggerWhen) == 0 {
		return fmt.Errorf("controller.detection_policy.trigger_when is required")
	}
	if len(controller.ProtectionPolicy) == 0 {
		return fmt.Errorf("controller.protection_policy is required")
	}
	if len(controller.AgentTrigger) == 0 {
		return fmt.Errorf("controller.agent_trigger is required")
	}
	policy := controller.RecoveryPolicy
	if strings.TrimSpace(policy.ID) == "" {
		return fmt.Errorf("controller.recovery_policy.id is required")
	}
	if policy.MinRequestsPerStep <= 0 {
		return fmt.Errorf("controller.recovery_policy.min_requests_per_step must be positive")
	}
	if policy.HealthyWindowsRequired <= 0 {
		return fmt.Errorf("controller.recovery_policy.healthy_windows_required must be positive")
	}
	if err := validateIncreasingRatios("controller.recovery_policy.probe_steps", policy.ProbeSteps); err != nil {
		return err
	}
	if err := validateIncreasingRatios("controller.recovery_policy.recovery_steps", policy.RecoverySteps); err != nil {
		return err
	}
	if policy.RecoverySteps[len(policy.RecoverySteps)-1] != 1 {
		return fmt.Errorf("controller.recovery_policy.recovery_steps must end at 1")
	}
	if len(policy.Require) == 0 {
		return fmt.Errorf("controller.recovery_policy.require is required")
	}
	if len(policy.HardStopWhen) == 0 {
		return fmt.Errorf("controller.recovery_policy.hard_stop_when is required")
	}
	return nil
}

func validateIncreasingRatios(field string, values []float64) error {
	if len(values) == 0 {
		return fmt.Errorf("%s is required", field)
	}
	previous := float64(0)
	for index, value := range values {
		if value <= 0 || value > 1 {
			return fmt.Errorf("%s[%d] must be within (0, 1]", field, index)
		}
		if index > 0 && value <= previous {
			return fmt.Errorf("%s must be strictly increasing", field)
		}
		previous = value
	}
	return nil
}

func validateAgentPolicy(policy AgentPolicy) error {
	if policy.RemediationCycleLimit <= 0 {
		return fmt.Errorf("agent_policy.remediation_cycle_limit must be positive")
	}
	if _, err := positiveDuration("agent_policy.incident_time_limit", policy.IncidentTimeLimit); err != nil {
		return err
	}
	if len(policy.ImmediateEscalationOn) == 0 {
		return fmt.Errorf("agent_policy.immediate_escalation_on is required")
	}
	return validateUniqueStrings("agent_policy.immediate_escalation_on", policy.ImmediateEscalationOn)
}

func validateTimeline(events []TimelineEvent, endAfter time.Duration) error {
	if len(events) == 0 {
		return fmt.Errorf("timeline must contain at least one event")
	}
	previous := time.Duration(-1)
	for index, event := range events {
		at, err := time.ParseDuration(event.At)
		if err != nil || at < 0 {
			return fmt.Errorf("timeline[%d].at %q is invalid", index, event.At)
		}
		if at < previous {
			return fmt.Errorf("timeline events must be ordered by time")
		}
		if at > endAfter {
			return fmt.Errorf("timeline[%d].at exceeds clock.end_after", index)
		}
		if strings.TrimSpace(event.Event) == "" {
			return fmt.Errorf("timeline[%d].event is required", index)
		}
		if strings.TrimSpace(event.Target) == "" {
			return fmt.Errorf("timeline[%d].target is required", index)
		}
		if len(event.Values) == 0 {
			return fmt.Errorf("timeline[%d].values is required", index)
		}
		previous = at
	}
	return nil
}

func validateObservation(observation Observation) error {
	if len(observation.ReadTools) == 0 {
		return fmt.Errorf("observation.read_tools is required")
	}
	return validateUniqueStrings("observation.read_tools", observation.ReadTools)
}

func validateTools(remediation []ToolDefinition, probe, escalation ToolDefinition) error {
	seen := make(map[string]struct{}, len(remediation)+2)
	for index, tool := range remediation {
		if err := validateTool(fmt.Sprintf("remediation_tools[%d]", index), tool, seen); err != nil {
			return err
		}
	}
	if err := validateTool("probe_tool", probe, seen); err != nil {
		return err
	}
	return validateTool("escalation_tool", escalation, seen)
}

func validateTool(field string, tool ToolDefinition, seen map[string]struct{}) error {
	name := strings.TrimSpace(tool.Name)
	if name == "" {
		return fmt.Errorf("%s.name is required", field)
	}
	if strings.TrimSpace(tool.Description) == "" {
		return fmt.Errorf("%s.description is required", field)
	}
	if _, exists := seen[name]; exists {
		return fmt.Errorf("duplicate tool name %q", name)
	}
	seen[name] = struct{}{}
	if len(tool.Arguments) == 0 {
		return fmt.Errorf("%s.arguments is required", field)
	}
	return validateUniqueStrings(field+".arguments", tool.Arguments)
}

func validateExpectations(document Expectations) error {
	if document.SchemaVersion != ExpectationSchemaV1_1 {
		return fmt.Errorf("unsupported expectation schema %q", document.SchemaVersion)
	}
	if strings.TrimSpace(document.ScenarioID) == "" {
		return fmt.Errorf("scenario_id is required")
	}
	sections := []struct {
		name  string
		value map[string]any
	}{
		{name: "controller", value: document.Controller},
		{name: "diagnosis", value: document.Diagnosis},
		{name: "remediation", value: document.Remediation},
		{name: "probe", value: document.Probe},
		{name: "recovery", value: document.Recovery},
		{name: "escalation", value: document.Escalation},
		{name: "experience", value: document.Experience},
	}
	for _, section := range sections {
		if len(section.value) == 0 {
			return fmt.Errorf("%s is required", section.name)
		}
	}
	return nil
}

func positiveDuration(field, value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s %q must be a positive duration", field, value)
	}
	return duration, nil
}

func validateUniqueStrings(field string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return fmt.Errorf("%s[%d] must not be empty", field, index)
		}
		if _, exists := seen[trimmed]; exists {
			return fmt.Errorf("%s contains duplicate value %q", field, trimmed)
		}
		seen[trimmed] = struct{}{}
	}
	return nil
}
