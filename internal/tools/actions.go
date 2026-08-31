package tools

import (
	"context"
	"fmt"
	"strings"

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

func buildActionTools(service platform.ToolOpsPlatform, incidentID string, gate EvidenceGate, authorizedTools []string, probePolicy platform.ProbeEscalationPolicy) ([]einotool.InvokableTool, error) {
	probe, err := toolutils.InferTool("request_probe", "请求控制器按策略执行小流量探测，可用于验证修复后的主路由或可用 fallback。健康主路由可进入 request_recovery；仅验证 fallback 时应回到 Agent、保持保护并按需升级，不能用探测直接结束事件。探测失败时停止恢复并继续调查。", func(ctx context.Context, input probeInput) (response[platform.Operation], error) {
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
	escalation, err := toolutils.InferTool("escalate_incident", "当没有安全修复方案、修复超过策略限制、需要更高权限或风险继续扩大时，提交证据和结构化 handoff 并升级人工处理。升级交接必须完整移交调查中已获得的全部维度证据：已查询过指标、日志、Trace 或配置状态的，对应 evidence_ref 都必须加入 evidence_refs，缺一会被拒绝。升级前是否必须探测由当前场景的机器策略决定：required 时即使授权修复目录为空也必须先探测；not_applicable 时不要在凭据无效、配额耗尽等确定不可探测状态下强行探测；conditional 由能力目录判断。credential_change_requires_human 仅用于已验证存在可用回退/替代但切换需人工执行的场景；凭据失效且无可行回退时应使用 no_safe_remediation_available。能力目录中全部授权修复动作都已尝试且未恢复时，必须如实使用 workflow_budget_exhausted。升级前已尝试过修复或探测动作的，把动作与结果摘要填入 handoff.attempted_actions。能力目录中 agent_authorized=false 的动作超出 Agent 权限，应写入 handoff.recommended_human_action。handoff 的受影响服务、当前保护状态和建议人工动作为必填。", func(ctx context.Context, input escalationInput) (response[platform.Operation], error) {
		if !input.ReasonCode.Valid() {
			return platformResponse(platform.Operation{}, fmt.Errorf(
				"reason_code %q 不在稳定类别列表中；只能使用 suspected_security_incident、possible_data_corruption、critical_telemetry_missing、no_safe_remediation_available、insufficient_permissions、credential_change_requires_human、rollback_failed、blast_radius_expanding 或 workflow_budget_exhausted",
				input.ReasonCode)), nil
		}
		if err := validateEscalationHandoff(input.ReasonCode, input.Handoff); err != nil {
			return platformResponse(platform.Operation{}, err), nil
		}
		if gate != nil {
			if err := gate.ValidateEscalationEvidence(input.ReasonCode, input.EvidenceRefs, authorizedTools, probePolicy); err != nil {
				return platformResponse(platform.Operation{}, err), nil
			}
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

// validateEscalationHandoff 在工具边界校验人工交接的结构完整性。受影响服务、
// 当前保护状态和建议人工动作是任何升级都必须携带的最小交接面；鉴权类升级
// 还必须带上鉴权证据，否则接手的安全团队无法复核判断依据。
func validateEscalationHandoff(reasonCode platform.EscalationReasonCode, handoff platform.EscalationHandoff) error {
	if strings.TrimSpace(handoff.AffectedService) == "" {
		return fmt.Errorf("handoff.affected_service 是必填字段：人工接手必须知道受影响的服务")
	}
	if handoff.CurrentProtectionState == nil {
		return fmt.Errorf("handoff.current_protection_state 是必填字段：人工接手必须知道当前熔断或降权状态")
	}
	if strings.TrimSpace(handoff.RecommendedHumanAction) == "" {
		return fmt.Errorf("handoff.recommended_human_action 是必填字段：必须明确建议人工执行的下一步")
	}
	if reasonCode == platform.EscalationReasonCredentialChangeRequiresHuman && len(handoff.AuthenticationEvidence) == 0 {
		return fmt.Errorf("reason_code=credential_change_requires_human 时 handoff.authentication_evidence 必填：安全团队复核凭据变更判断需要鉴权证据引用")
	}
	return nil
}
