// Package platform 定义 ToolOps Agent 与受管平台之间的平台无关契约。
package platform

import "context"

// ToolOpsPlatform 是 Agent、Simulator 和真实平台 Adapter 共同遵守的接口。
// 查询方法必须只读，写方法只能执行预先注册且受 Policy 约束的高层动作。
// 所有写方法必须把 IdempotencyKey 作为业务幂等键：相同键和相同动作的重复请求
// 必须返回同一个 Operation，不能再次产生生产副作用；相同键用于不同动作必须报冲突。
// 长耗时写操作应快速返回 pending/running Operation，由 Controller 后续调用 GetOperation 轮询。
type ToolOpsPlatform interface {
	QueryMetrics(ctx context.Context, query MetricQuery) (MetricResult, error)
	QueryLogs(ctx context.Context, query LogQuery) ([]LogEntry, error)
	QueryTraces(ctx context.Context, query TraceQuery) ([]TraceRecord, error)
	GetServices(ctx context.Context, query StateQuery) ([]Service, error)
	GetRoutes(ctx context.Context, query StateQuery) ([]Route, error)
	GetProviders(ctx context.Context, query StateQuery) ([]Provider, error)
	GetConfigVersions(ctx context.Context, query StateQuery) ([]ConfigVersion, error)
	GetChangeRecords(ctx context.Context, query ChangeQuery) ([]ChangeRecord, error)
	GetCredentialMetadata(ctx context.Context, query StateQuery) ([]CredentialMetadata, error)
	GetConnectionMetadata(ctx context.Context, query StateQuery) ([]ConnectionMetadata, error)
	GetTasks(ctx context.Context, query TaskQuery) ([]ManagedTask, error)
	GetRemediationCapabilities(ctx context.Context, query StateQuery) ([]RemediationCapability, error)
	GetRecoveryPolicies(ctx context.Context, query StateQuery) ([]RecoveryPolicy, error)

	ExecuteRemediation(ctx context.Context, request RemediationRequest) (Operation, error)
	RequestProbe(ctx context.Context, request ProbeRequest) (Operation, error)
	RequestRecovery(ctx context.Context, request RecoveryRequest) (Operation, error)
	GetOperation(ctx context.Context, query OperationQuery) (Operation, error)
	EscalateIncident(ctx context.Context, request EscalationRequest) (Operation, error)
}
