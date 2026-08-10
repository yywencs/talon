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
// 它同时描述根因证据、唯一修复动作以及后续探测和恢复策略，避免 Agent
// 只提交一段无法被 Workflow 校验和执行的自然语言计划。
type submitPlanInput struct {
	Summary              string         `json:"summary" jsonschema:"required,description=计划摘要和预期结果"`
	RootCause            string         `json:"root_cause" jsonschema:"required,description=由证据支持的根因判断"`
	EvidenceRefs         []string       `json:"evidence_refs" jsonschema:"required,description=支持根因和修复选择的证据引用"`
	RemediationTool      string         `json:"remediation_tool" jsonschema:"required,description=准备执行的已注册修复工具名称"`
	RemediationArguments map[string]any `json:"remediation_arguments" jsonschema:"required,description=修复工具参数，不包含额外工具调用"`
	ProbeRouteID         string         `json:"probe_route_id" jsonschema:"required,description=修复成功后需要探测的路由ID"`
	RecoveryPolicyID     string         `json:"recovery_policy_id" jsonschema:"required,description=探测和恢复使用的确定性策略ID"`
}

// newSubmitPlanTool 创建提交修复计划的 Eino 工具。
// remediations 是当前 Incident 允许使用的修复能力快照；工具只负责校验并冻结计划，
// 不会在提交过程中执行修复。计划提交成功后，Workflow 会从 investigating 转为 planned。
func newSubmitPlanTool(instance *workflow.IncidentWorkflow, remediations map[string]platform.RemediationCapability) (einotool.InvokableTool, error) {
	tool, err := toolutils.InferTool(
		"submit_plan",
		"证据足够后提交一份冻结的结构化修复计划。该工具只保存计划并推进到planned，不会直接执行修复；无安全修复方案时应升级人工。",
		func(_ context.Context, input submitPlanInput) (response[workflow.PlanSubmission], error) {
			// 修复工具必须来自平台为当前 Incident 提供的能力目录，不能由模型任意指定。
			capability, allowed := remediations[input.RemediationTool]
			if !allowed {
				return response[workflow.PlanSubmission]{}, fmt.Errorf("remediation tool %q is not available for this incident", input.RemediationTool)
			}
			// 参数只能包含该修复能力声明过的字段，并且必须满足能力本身的约束。
			if err := validateRemediationArguments(capability, input.RemediationArguments); err != nil {
				return response[workflow.PlanSubmission]{}, fmt.Errorf("validate planned remediation: %w", err)
			}
			// capability.Arguments 中声明的参数全部视为必填，防止执行阶段才发现计划不完整。
			for _, name := range capability.Arguments {
				if _, exists := input.RemediationArguments[name]; !exists {
					return response[workflow.PlanSubmission]{}, fmt.Errorf("planned remediation argument %q is required", name)
				}
			}
			// 每个修复计划必须携带幂等键，避免重试或恢复运行时重复执行同一项修复。
			if key, ok := input.RemediationArguments["idempotency_key"].(string); !ok || strings.TrimSpace(key) == "" {
				return response[workflow.PlanSubmission]{}, fmt.Errorf("planned remediation idempotency_key must be a non-empty string")
			}
			// SubmitPlan 会再次校验 Workflow 权限，并原子地冻结计划和推进状态。
			result, submitErr := instance.SubmitPlan(workflow.PlanDraft{
				Summary: input.Summary, RootCause: input.RootCause, EvidenceRefs: input.EvidenceRefs,
				Remediation:  workflow.PlannedAction{ToolName: input.RemediationTool, Arguments: input.RemediationArguments},
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
