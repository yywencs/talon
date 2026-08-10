package platform

import "time"

// Scope 将所有查询限制在当前 Incident 及其受影响资源内。
type Scope struct {
	IncidentID string `json:"incident_id"`
	ServiceID  string `json:"service_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	RouteID    string `json:"route_id,omitempty"`
	ProviderID string `json:"provider_id,omitempty"`
}

// TimeRange 表示查询使用的闭区间时间范围。
type TimeRange struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// MetricName 是平台统一使用的指标名称。
type MetricName string

const (
	MetricSuccessRate             MetricName = "success_rate"
	MetricErrorRate               MetricName = "error_rate"
	MetricLatencyP95MS            MetricName = "latency_p95_ms"
	MetricCostPerSuccess          MetricName = "cost_per_success"
	MetricAuthenticationErrorRate MetricName = "authentication_error_rate"
	MetricConnectionErrorRate     MetricName = "connection_error_rate"
)

// MetricQuery 描述一次按范围和维度执行的指标查询。
type MetricQuery struct {
	Scope Scope        `json:"scope"`
	Range TimeRange    `json:"range"`
	Names []MetricName `json:"names,omitempty"`
}

// MetricPoint 表示某个时间点的指标值。
type MetricPoint struct {
	At         time.Time         `json:"at"`
	Name       MetricName        `json:"name"`
	Value      float64           `json:"value"`
	Unit       string            `json:"unit,omitempty"`
	Dimensions map[string]string `json:"dimensions,omitempty"`
}

// MetricResult 包含指标点、样本量和遥测完整性。
type MetricResult struct {
	Points      []MetricPoint `json:"points"`
	SampleCount int           `json:"sample_count"`
	Complete    bool          `json:"complete"`
}

// LogQuery 描述一次受时间范围和返回数量约束的日志查询。
type LogQuery struct {
	Scope Scope     `json:"scope"`
	Range TimeRange `json:"range"`
	Limit int       `json:"limit,omitempty"`
}

// LogEntry 是已经完成权限过滤和脱敏的日志记录。
type LogEntry struct {
	At         time.Time      `json:"at"`
	Level      string         `json:"level"`
	Component  string         `json:"component"`
	Code       string         `json:"code,omitempty"`
	Message    string         `json:"message"`
	Scope      Scope          `json:"scope"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// TraceQuery 描述一次 Trace 样本查询。
type TraceQuery struct {
	Scope Scope     `json:"scope"`
	Range TimeRange `json:"range"`
	Limit int       `json:"limit,omitempty"`
}

// TraceRecord 是允许 Agent 查看的一条脱敏 Trace 摘要。
type TraceRecord struct {
	ID           string         `json:"id"`
	At           time.Time      `json:"at"`
	Scope        Scope          `json:"scope"`
	TerminalSpan string         `json:"terminal_span,omitempty"`
	Status       string         `json:"status,omitempty"`
	Attributes   map[string]any `json:"attributes,omitempty"`
}

// StateQuery 描述路由、Provider、配置和连接状态的只读查询。
type StateQuery struct {
	Scope Scope `json:"scope"`
}

// ChangeQuery 描述变更记录查询。
type ChangeQuery struct {
	Scope Scope     `json:"scope"`
	Range TimeRange `json:"range"`
}

// SLO 定义工具运行质量的确定性门槛。
type SLO struct {
	SuccessRateMin    float64  `json:"success_rate_min"`
	LatencyP95MSMax   int64    `json:"latency_p95_ms_max"`
	CostPerSuccessMax *float64 `json:"cost_per_success_max,omitempty"`
}

// Service 表示受管服务及其工具。
type Service struct {
	ID    string          `json:"id"`
	Tools map[string]Tool `json:"tools"`
}

// Tool 表示一个可被路由的外部工具能力。
type Tool struct {
	Name     string   `json:"name"`
	SLO      SLO      `json:"slo"`
	RouteIDs []string `json:"route_ids"`
}

// Route 表示工具到 Provider 的一条流量路由。
type Route struct {
	ID                string `json:"id"`
	ServiceID         string `json:"service_id"`
	ToolName          string `json:"tool_name"`
	ProviderID        string `json:"provider_id"`
	BaselineWeight    int    `json:"baseline_weight"`
	Weight            int    `json:"weight"`
	Enabled           bool   `json:"enabled"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

// ProviderHealth 是统一的 Provider 健康状态。
type ProviderHealth string

const (
	ProviderHealthy     ProviderHealth = "healthy"
	ProviderDegraded    ProviderHealth = "degraded"
	ProviderUnavailable ProviderHealth = "unavailable"
)

// Provider 表示外部能力提供方的当前状态。
type Provider struct {
	ID               string         `json:"id"`
	Health           ProviderHealth `json:"health"`
	Endpoint         string         `json:"endpoint,omitempty"`
	SchemaCompatible *bool          `json:"schema_compatible,omitempty"`
}

// TrafficProfile 保存故障发生前的流量和指标基线。
type TrafficProfile struct {
	RequestsPerMinute int      `json:"requests_per_minute"`
	SuccessRate       float64  `json:"success_rate"`
	LatencyP95MS      int64    `json:"latency_p95_ms"`
	CostPerSuccess    *float64 `json:"cost_per_success,omitempty"`
}

// ConfigVersion 表示一个不可变配置版本及其健康属性。
type ConfigVersion struct {
	ID           string         `json:"id"`
	Active       bool           `json:"active"`
	KnownHealthy bool           `json:"known_healthy"`
	Attributes   map[string]any `json:"attributes,omitempty"`
}

// CredentialMetadata 只包含凭证元数据，永远不包含凭证值。
type CredentialMetadata struct {
	ProviderID   string `json:"provider_id"`
	CredentialID string `json:"credential_id"`
	Status       string `json:"status"`
	ManagedBy    string `json:"managed_by,omitempty"`
}

// ConnectionMetadata 表示 Provider 连接池和解析缓存的可观测状态。
type ConnectionMetadata struct {
	ProviderID              string         `json:"provider_id"`
	PoolGeneration          int            `json:"pool_generation"`
	ResolverCacheGeneration int            `json:"resolver_cache_generation"`
	ResolvedIP              string         `json:"resolved_ip,omitempty"`
	ActiveConnections       int            `json:"active_connections"`
	TargetConnections       int            `json:"target_connections"`
	ConfigFingerprint       string         `json:"config_fingerprint,omitempty"`
	LastPingAt              *time.Time     `json:"last_ping_at,omitempty"`
	Attributes              map[string]any `json:"attributes,omitempty"`
}

// TaskStatus 与 AgentPlatform 异步任务引擎的状态机保持相同语义。
type TaskStatus string

const (
	TaskCreated    TaskStatus = "created"
	TaskProcessing TaskStatus = "processing"
	TaskFinished   TaskStatus = "finished"
	TaskFailed     TaskStatus = "failed"
	TaskCanceled   TaskStatus = "canceled"
)

// ManagedTask 表示可被 Agent 查询和受控补偿的异步任务。
type ManagedTask struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Name       string         `json:"name"`
	Status     TaskStatus     `json:"status"`
	ProviderID string         `json:"provider_id,omitempty"`
	Attempts   int            `json:"attempts"`
	Idempotent bool           `json:"idempotent"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	LastError  string         `json:"last_error,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// TaskQuery 描述异步任务状态查询。
type TaskQuery struct {
	Scope    Scope        `json:"scope"`
	Statuses []TaskStatus `json:"statuses,omitempty"`
}

// RemediationCapability 是允许向 Agent 暴露的安全修复函数说明。
// 它不包含场景内部动作结果、隐藏根因或凭证材料。
type RemediationCapability struct {
	Name             string         `json:"name"`
	Description      string         `json:"description"`
	Risk             string         `json:"risk,omitempty"`
	RequiresApproval bool           `json:"requires_approval"`
	Arguments        []string       `json:"arguments"`
	Preconditions    map[string]any `json:"preconditions,omitempty"`
}

// ChangeRecord 表示发布、配置或人工操作产生的审计记录。
type ChangeRecord struct {
	ID         string         `json:"id"`
	At         time.Time      `json:"at"`
	Kind       string         `json:"kind"`
	Scope      Scope          `json:"scope"`
	Actor      string         `json:"actor,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// OperationKind 表示一次受控写操作的类型。
type OperationKind string

const (
	OperationRemediation OperationKind = "remediation"
	OperationProbe       OperationKind = "probe"
	OperationRecovery    OperationKind = "recovery"
	OperationEscalation  OperationKind = "escalation"
)

// OperationStatus 表示异步操作的生命周期状态。
type OperationStatus string

const (
	OperationPending   OperationStatus = "pending"
	OperationRunning   OperationStatus = "running"
	OperationSucceeded OperationStatus = "succeeded"
	OperationFailed    OperationStatus = "failed"
	OperationRejected  OperationStatus = "rejected"
	OperationCancelled OperationStatus = "cancelled"
)

// Operation 是修复、探测、恢复和升级共用的可审计操作记录。
type Operation struct {
	ID             string          `json:"id"`
	IncidentID     string          `json:"incident_id"`
	Kind           OperationKind   `json:"kind"`
	Name           string          `json:"name"`
	Status         OperationStatus `json:"status"`
	IdempotencyKey string          `json:"idempotency_key"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	Message        string          `json:"message,omitempty"`
	Result         map[string]any  `json:"result,omitempty"`
}

// RemediationRequest 请求执行一个预先注册的修复函数。
type RemediationRequest struct {
	IncidentID      string         `json:"incident_id"`
	ToolName        string         `json:"tool_name"`
	Arguments       map[string]any `json:"arguments"`
	ExpectedVersion string         `json:"expected_version,omitempty"`
	DryRun          bool           `json:"dry_run"`
	IdempotencyKey  string         `json:"idempotency_key"`
}

// ProbeRequest 请求控制器按照指定策略发起小流量探测。
type ProbeRequest struct {
	IncidentID     string `json:"incident_id"`
	RouteID        string `json:"route_id"`
	PolicyID       string `json:"policy_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

// RecoveryRequest 请求控制器进入逐级恢复流程。
type RecoveryRequest struct {
	IncidentID     string `json:"incident_id"`
	PolicyID       string `json:"policy_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

// OperationQuery 查询一项已经提交的异步操作。
type OperationQuery struct {
	IncidentID  string `json:"incident_id"`
	OperationID string `json:"operation_id"`
}

// EscalationRequest 将当前事件和结构化上下文交给人工处理。
type EscalationRequest struct {
	IncidentID            string         `json:"incident_id"`
	Reason                string         `json:"reason"`
	EvidenceRefs          []string       `json:"evidence_refs"`
	AttemptedOperationIDs []string       `json:"attempted_operation_ids,omitempty"`
	ProtectionState       map[string]any `json:"protection_state,omitempty"`
	IdempotencyKey        string         `json:"idempotency_key"`
}
