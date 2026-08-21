package tools

import (
	"context"
	"fmt"

	einotool "github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/wen/opentalon/internal/platform"
)

type probeInput struct {
	RouteID        string `json:"route_id" jsonschema:"required,description=需要进行小流量探测的路由ID"`
	PolicyID       string `json:"policy_id" jsonschema:"required,description=控制器恢复策略ID"`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"required,description=本次探测请求的唯一幂等键"`
}

type recoveryInput struct {
	RouteID        string `json:"route_id" jsonschema:"required,description=已通过健康探测且需要逐级恢复的路由ID"`
	PolicyID       string `json:"policy_id" jsonschema:"required,description=已通过健康探测的恢复策略ID"`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"required,description=本次恢复请求的唯一幂等键"`
}

type operationInput struct {
	OperationID string `json:"operation_id" jsonschema:"required,description=需要查询的异步操作ID"`
}

type escalationInput struct {
	ReasonCode            platform.EscalationReasonCode `json:"reason_code" jsonschema:"required,description=停止自治并升级的稳定类别；只能使用 suspected_security_incident、possible_data_corruption、critical_telemetry_missing、no_safe_remediation_available、insufficient_permissions、credential_change_requires_human、rollback_failed、blast_radius_expanding 或 workflow_budget_exhausted"`
	Reason                string                        `json:"reason" jsonschema:"required,description=升级人工的明确原因"`
	EvidenceRefs          []string                      `json:"evidence_refs" jsonschema:"required,description=支持判断的日志、Trace、指标或状态引用"`
	AttemptedOperationIDs []string                      `json:"attempted_operation_ids,omitempty" jsonschema:"description=已经尝试过的修复或探测操作ID"`
	ProtectionState       map[string]any                `json:"protection_state,omitempty" jsonschema:"description=当前熔断或降权保护状态"`
	Handoff               platform.EscalationHandoff    `json:"handoff" jsonschema:"required,description=结构化人工交接；必须填写受影响服务、当前保护状态和建议人工动作，鉴权故障还必须填写鉴权证据及无可用回退原因"`
	IdempotencyKey        string                        `json:"idempotency_key" jsonschema:"required,description=本次升级请求的唯一幂等键"`
}

func buildActionTools(service platform.ToolOpsPlatform, incidentID string) ([]einotool.InvokableTool, error) {
	probe, err := toolutils.InferTool("request_probe", "修复完成后，请求控制器按策略执行小流量探测。Agent不能自行修改流量权重；探测失败时必须停止恢复并继续调查。", func(ctx context.Context, input probeInput) (response[platform.Operation], error) {
		result, callErr := service.RequestProbe(ctx, platform.ProbeRequest{
			IncidentID: incidentID, RouteID: input.RouteID, PolicyID: input.PolicyID, IdempotencyKey: input.IdempotencyKey,
		})
		return platformResponse(result, callErr), nil
	})
	if err != nil {
		return nil, fmt.Errorf("build request_probe tool: %w", err)
	}
	recovery, err := toolutils.InferTool("request_recovery", "仅在最近一次小流量探测健康后，请求控制器按照恢复策略逐级恢复路由权重。", func(ctx context.Context, input recoveryInput) (response[platform.Operation], error) {
		result, callErr := service.RequestRecovery(ctx, platform.RecoveryRequest{
			IncidentID: incidentID, RouteID: input.RouteID, PolicyID: input.PolicyID, IdempotencyKey: input.IdempotencyKey,
		})
		return platformResponse(result, callErr), nil
	})
	if err != nil {
		return nil, fmt.Errorf("build request_recovery tool: %w", err)
	}
	getOperation, err := toolutils.InferTool("get_operation", "查询修复、探测、恢复或升级操作的当前状态。异步修复提交后应使用此工具确认完成状态。", func(ctx context.Context, input operationInput) (response[platform.Operation], error) {
		result, callErr := service.GetOperation(ctx, platform.OperationQuery{IncidentID: incidentID, OperationID: input.OperationID})
		return platformResponse(result, callErr), nil
	})
	if err != nil {
		return nil, fmt.Errorf("build get_operation tool: %w", err)
	}
	escalation, err := toolutils.InferTool("escalate_incident", "当没有安全修复方案、修复超过策略限制、需要更高权限或风险继续扩大时，提交证据和结构化 handoff 并升级人工处理。", func(ctx context.Context, input escalationInput) (response[platform.Operation], error) {
		if !input.ReasonCode.Valid() {
			return platformResponse(platform.Operation{}, fmt.Errorf(
				"reason_code %q 不在稳定类别列表中；只能使用 suspected_security_incident、possible_data_corruption、critical_telemetry_missing、no_safe_remediation_available、insufficient_permissions、credential_change_requires_human、rollback_failed、blast_radius_expanding 或 workflow_budget_exhausted",
				input.ReasonCode)), nil
		}
		result, callErr := service.EscalateIncident(ctx, platform.EscalationRequest{
			IncidentID: incidentID, ReasonCode: input.ReasonCode, Reason: input.Reason, EvidenceRefs: input.EvidenceRefs,
			AttemptedOperationIDs: input.AttemptedOperationIDs, ProtectionState: input.ProtectionState,
			Handoff:        input.Handoff,
			IdempotencyKey: input.IdempotencyKey,
		})
		return platformResponse(result, callErr), nil
	})
	if err != nil {
		return nil, fmt.Errorf("build escalate_incident tool: %w", err)
	}
	return []einotool.InvokableTool{probe, recovery, getOperation, escalation}, nil
}
