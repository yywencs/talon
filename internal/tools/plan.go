package tools

import (
	"context"
	"fmt"
	"strings"

	einotool "github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/workflow"
)

// submitPlanInput 是模型调用 submit_plan 时必须提供的结构化参数。
// 它同时描述根因证据、修复动作序列以及后续探测和恢复策略，避免 Agent
// 只提交一段无法被 Workflow 校验和执行的自然语言计划。
type submitPlanActionInput struct {
	ToolName  string         `json:"tool_name" jsonschema:"required,description=准备执行的已注册修复工具名称"`
	Arguments map[string]any `json:"arguments" jsonschema:"required,description=该修复工具的完整参数"`
}

type submitPlanInput struct {
	Summary          string                  `json:"summary" jsonschema:"required,description=计划摘要和预期结果"`
	RootCause        string                  `json:"root_cause" jsonschema:"required,description=由证据支持的根因判断"`
	EvidenceRefs     []string                `json:"evidence_refs" jsonschema:"required,description=支持根因和修复选择的证据引用"`
	Actions          []submitPlanActionInput `json:"actions" jsonschema:"required,description=按执行顺序排列的一个或多个具体修复动作"`
	ProbeRouteID     string                  `json:"probe_route_id" jsonschema:"required,description=修复成功后需要探测的路由ID"`
	RecoveryPolicyID string                  `json:"recovery_policy_id" jsonschema:"required,description=必须原样引用get_recovery_policies返回的确定性策略ID，不得自行生成"`
}

// newSubmitPlanTool 创建提交修复计划的 Eino 工具。
// remediations 是当前 Incident 允许使用的修复能力快照；工具只负责校验并冻结计划，
// 不会在提交过程中执行修复。计划提交成功后，Workflow 会从 investigating 转为 planned。
func newSubmitPlanTool(instance *workflow.IncidentWorkflow, service platform.ToolOpsPlatform, incidentID string, remediations map[string]platform.RemediationCapability) (einotool.InvokableTool, error) {
	tool, err := toolutils.InferTool(
		"submit_plan",
		"证据足够后提交一份冻结的结构化修复计划。提交前必须调用get_recovery_policies并引用其返回的策略ID。该工具只保存计划并推进到planned，不会直接执行修复；无安全修复方案时应升级人工。",
		func(ctx context.Context, input submitPlanInput) (response[workflow.PlanSubmission], error) {
			if len(input.Actions) == 0 {
				return response[workflow.PlanSubmission]{}, fmt.Errorf("plan actions is required")
			}
			actions := make([]workflow.PlannedAction, 0, len(input.Actions))
			for index, action := range input.Actions {
				// 每个修复工具都必须来自平台为当前 Incident 提供的能力目录。
				capability, allowed := remediations[action.ToolName]
				if !allowed {
					return response[workflow.PlanSubmission]{}, fmt.Errorf("action %d remediation tool %q is not available for this incident", index, action.ToolName)
				}
				if err := validateRemediationArguments(capability, action.Arguments); err != nil {
					return response[workflow.PlanSubmission]{}, fmt.Errorf("validate planned action %d: %w", index, err)
				}
				for _, name := range capability.Arguments {
					if _, exists := action.Arguments[name]; !exists {
						return response[workflow.PlanSubmission]{}, fmt.Errorf("planned action %d argument %q is required", index, name)
					}
				}
				// 每个 Action 使用独立幂等键，避免部分重试时重复执行其他动作。
				if key, ok := action.Arguments["idempotency_key"].(string); !ok || strings.TrimSpace(key) == "" {
					return response[workflow.PlanSubmission]{}, fmt.Errorf("planned action %d idempotency_key must be a non-empty string", index)
				}
				actions = append(actions, workflow.PlannedAction{ToolName: action.ToolName, Arguments: action.Arguments})
			}
			// 恢复策略必须来自 Platform 的只读策略目录，不能让模型凭空构造一个 ID。
			policies, policyErr := service.GetRecoveryPolicies(ctx, platform.StateQuery{
				Scope: platform.Scope{IncidentID: incidentID},
			})
			if policyErr != nil {
				return platformResponse(workflow.PlanSubmission{}, fmt.Errorf("get recovery policies: %w", policyErr)), nil
			}
			policyID := strings.TrimSpace(input.RecoveryPolicyID)
			policyAllowed := false
			for _, policy := range policies {
				if policy.ID == policyID {
					policyAllowed = true
					break
				}
			}
			if !policyAllowed {
				return platformResponse(workflow.PlanSubmission{}, fmt.Errorf("recovery policy %q is not available for this incident; call get_recovery_policies and use an exact returned ID", policyID)), nil
			}
			// SubmitPlan 会再次校验 Workflow 权限，并原子地冻结计划和推进状态。
			result, submitErr := instance.SubmitPlan(workflow.PlanDraft{
				Summary: input.Summary, RootCause: input.RootCause, EvidenceRefs: input.EvidenceRefs,
				Actions:      actions,
				ProbeRouteID: input.ProbeRouteID, RecoveryPolicyID: input.RecoveryPolicyID,
			})
			return platformResponse(result, submitErr), nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("build submit_plan tool: %w", err)
	}
	return tool, nil
}
