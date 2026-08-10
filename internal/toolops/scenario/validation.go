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
	if len(document.InitialState) == 0 {
		return fmt.Errorf("initial_state is required")
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
	if len(policy.Require) == 0 {
		return fmt.Errorf("controller.recovery_policy.require is required")
	}
	if len(policy.HardStopWhen) == 0 {
		return fmt.Errorf("controller.recovery_policy.hard_stop_when is required")
	}
	return nil
}

func validateIncreasingRatios(field string, values []float64) error {
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
