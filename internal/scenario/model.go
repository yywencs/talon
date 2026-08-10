// Package scenario 负责加载和校验带版本的 ToolOps Simulator 数据集。
package scenario

const (
	ScenarioSchemaV1_1    = "toolops-scenario/v1.1"
	ExpectationSchemaV1_1 = "toolops-expectation/v1.1"
)

// Dataset 表示由场景和期望结果配对组成的确定性数据集。
type Dataset struct {
	Root  string
	Cases []Case
}

// Find 根据稳定的场景 ID 查找对应的场景数据和期望结果。
func (d *Dataset) Find(id string) (*Case, bool) {
	if d == nil {
		return nil, false
	}
	for index := range d.Cases {
		if d.Cases[index].Scenario.Metadata.ID == id {
			return &d.Cases[index], true
		}
	}
	return nil, false
}

// Case 将 Simulator 的虚拟世界与仅供 Evaluator 使用的期望结果组成一组数据。
type Case struct {
	Directory    string
	Scenario     Scenario
	Expectations Expectations
}

// Scenario 描述完整的 Simulator 虚拟世界。Timeline 可能包含隐藏的内部字段，
// 因此禁止将 Scenario 直接传给 Agent。
type Scenario struct {
	SchemaVersion    string                    `yaml:"schema_version"`
	Metadata         Metadata                  `yaml:"metadata"`
	Clock            Clock                     `yaml:"clock"`
	InitialState     InitialState              `yaml:"initial_state"`
	Controller       Controller                `yaml:"controller"`
	AgentPolicy      AgentPolicy               `yaml:"agent_policy"`
	Timeline         []TimelineEvent           `yaml:"timeline"`
	Observation      Observation               `yaml:"observation"`
	RemediationTools []ToolDefinition          `yaml:"remediation_tools"`
	ProbeTool        ToolDefinition            `yaml:"probe_tool"`
	EscalationTool   ToolDefinition            `yaml:"escalation_tool"`
	ActionBehavior   map[string]map[string]any `yaml:"action_behavior"`
}

type Metadata struct {
	ID         string   `yaml:"id"`
	Title      string   `yaml:"title"`
	Category   string   `yaml:"category"`
	Difficulty string   `yaml:"difficulty"`
	Tags       []string `yaml:"tags"`
}

type Clock struct {
	StartAt  string `yaml:"start_at"`
	Tick     string `yaml:"tick"`
	EndAfter string `yaml:"end_after"`
}

// InitialState 描述故障发生前用于初始化 Simulator World 的状态。
type InitialState struct {
	Service            InitialService               `yaml:"service"`
	Providers          []InitialProvider            `yaml:"providers"`
	Traffic            InitialTraffic               `yaml:"traffic"`
	Config             *InitialConfig               `yaml:"config"`
	CredentialMetadata *InitialCredentialMetadata   `yaml:"credential_metadata"`
	Connection         map[string]InitialConnection `yaml:"connection"`
	Tasks              []InitialTask                `yaml:"tasks"`
}

type InitialService struct {
	ID     string         `yaml:"id"`
	Tool   string         `yaml:"tool"`
	SLO    InitialSLO     `yaml:"slo"`
	Routes []InitialRoute `yaml:"routes"`
}

type InitialSLO struct {
	SuccessRateMin    float64  `yaml:"success_rate_min"`
	LatencyP95MSMax   int64    `yaml:"latency_p95_ms_max"`
	CostPerSuccessMax *float64 `yaml:"cost_per_success_max"`
}

type InitialRoute struct {
	ID                string `yaml:"id"`
	Provider          string `yaml:"provider"`
	Weight            int    `yaml:"weight"`
	Enabled           bool   `yaml:"enabled"`
	UnavailableReason string `yaml:"unavailable_reason"`
}

type InitialProvider struct {
	ID               string `yaml:"id"`
	Health           string `yaml:"health"`
	Endpoint         string `yaml:"endpoint"`
	SchemaCompatible *bool  `yaml:"schema_compatible"`
}

type InitialTraffic struct {
	RequestsPerMinute int      `yaml:"requests_per_minute"`
	SuccessRate       float64  `yaml:"success_rate"`
	LatencyP95MS      int64    `yaml:"latency_p95_ms"`
	CostPerSuccess    *float64 `yaml:"cost_per_success"`
}

type InitialConfig struct {
	CurrentVersion       string   `yaml:"current_version"`
	KnownHealthyVersions []string `yaml:"known_healthy_versions"`
}

type InitialCredentialMetadata struct {
	Provider      string `yaml:"provider"`
	CredentialID  string `yaml:"credential_id"`
	Status        string `yaml:"status"`
	SecretVisible bool   `yaml:"secret_visible"`
	ManagedBy     string `yaml:"managed_by"`
}

type InitialConnection struct {
	PoolGeneration          int    `yaml:"pool_generation"`
	ResolverCacheGeneration int    `yaml:"resolver_cache_generation"`
	ResolvedIP              string `yaml:"resolved_ip"`
}

type InitialTask struct {
	ID         string         `yaml:"id"`
	Type       string         `yaml:"type"`
	Name       string         `yaml:"name"`
	Status     string         `yaml:"status"`
	ProviderID string         `yaml:"provider_id"`
	Attempts   int            `yaml:"attempts"`
	Idempotent bool           `yaml:"idempotent"`
	LastError  string         `yaml:"last_error"`
	Attributes map[string]any `yaml:"attributes"`
}

type Controller struct {
	DetectionPolicy  DetectionPolicy `yaml:"detection_policy"`
	ProtectionPolicy map[string]any  `yaml:"protection_policy"`
	AgentTrigger     map[string]any  `yaml:"agent_trigger"`
	RecoveryPolicy   RecoveryPolicy  `yaml:"recovery_policy"`
}

type DetectionPolicy struct {
	Window      string         `yaml:"window"`
	MinRequests int            `yaml:"min_requests"`
	TriggerWhen map[string]any `yaml:"trigger_when"`
}

type RecoveryPolicy struct {
	ID                     string         `yaml:"id"`
	ProbeSteps             []float64      `yaml:"probe_steps"`
	RecoverySteps          []float64      `yaml:"recovery_steps"`
	StepMode               string         `yaml:"step_mode"`
	MinRequestsPerStep     int            `yaml:"min_requests_per_step"`
	HealthyWindowsRequired int            `yaml:"healthy_windows_required"`
	Require                map[string]any `yaml:"require"`
	HardStopWhen           map[string]any `yaml:"hard_stop_when"`
}

type AgentPolicy struct {
	RemediationCycleLimit         int      `yaml:"remediation_cycle_limit"`
	IncidentTimeLimit             string   `yaml:"incident_time_limit"`
	RequireNewEvidenceBeforeRetry bool     `yaml:"require_new_evidence_before_retry"`
	ImmediateEscalationOn         []string `yaml:"immediate_escalation_on"`
}

type TimelineEvent struct {
	At     string         `yaml:"at"`
	Event  string         `yaml:"event"`
	Target string         `yaml:"target"`
	Values map[string]any `yaml:"values"`
}

type Observation struct {
	ReadTools          []string       `yaml:"read_tools"`
	Metrics            map[string]any `yaml:"metrics"`
	Logs               map[string]any `yaml:"logs"`
	Traces             map[string]any `yaml:"traces"`
	Changes            map[string]any `yaml:"changes"`
	Credentials        map[string]any `yaml:"credentials"`
	ConnectionMetadata map[string]any `yaml:"connection_metadata"`
}

type ToolDefinition struct {
	Name                        string         `yaml:"name"`
	Description                 string         `yaml:"description"`
	Risk                        string         `yaml:"risk"`
	RequiresApproval            bool           `yaml:"requires_approval"`
	AllowedCallers              []string       `yaml:"allowed_callers"`
	AgentAuthorized             *bool          `yaml:"agent_authorized"`
	Arguments                   []string       `yaml:"arguments"`
	Preconditions               map[string]any `yaml:"preconditions"`
	Effect                      map[string]any `yaml:"effect"`
	CompensatingAction          string         `yaml:"compensating_action"`
	ControllerOwnsTrafficWeight bool           `yaml:"controller_owns_traffic_weight"`
	Note                        string         `yaml:"note"`
}

// Expectations 是仅供 Evaluator 使用的数据，禁止向 Agent 暴露。
// 不同场景需要校验的执行路径不同，因此各部分保留可扩展结构。
type Expectations struct {
	SchemaVersion string         `yaml:"schema_version"`
	ScenarioID    string         `yaml:"scenario_id"`
	Controller    map[string]any `yaml:"controller"`
	Diagnosis     map[string]any `yaml:"diagnosis"`
	Remediation   map[string]any `yaml:"remediation"`
	Probe         map[string]any `yaml:"probe"`
	Recovery      map[string]any `yaml:"recovery"`
	Escalation    map[string]any `yaml:"escalation"`
	Experience    map[string]any `yaml:"experience"`
}
