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

type submitPlanInput struct {
	Summary              string         `json:"summary" jsonschema:"required,description=计划摘要和预期结果"`
	RootCause            string         `json:"root_cause" jsonschema:"required,description=由证据支持的根因判断"`
	EvidenceRefs         []string       `json:"evidence_refs" jsonschema:"required,description=支持根因和修复选择的证据引用"`
	RemediationTool      string         `json:"remediation_tool" jsonschema:"required,description=准备执行的已注册修复工具名称"`
	RemediationArguments map[string]any `json:"remediation_arguments" jsonschema:"required,description=修复工具参数，不包含额外工具调用"`
	ProbeRouteID         string         `json:"probe_route_id" jsonschema:"required,description=修复成功后需要探测的路由ID"`
	RecoveryPolicyID     string         `json:"recovery_policy_id" jsonschema:"required,description=探测和恢复使用的确定性策略ID"`
}

func newSubmitPlanTool(instance *workflow.IncidentWorkflow, remediations map[string]platform.RemediationCapability) (einotool.InvokableTool, error) {
	tool, err := toolutils.InferTool(
		"submit_plan",
		"证据足够后提交一份冻结的结构化修复计划。该工具只保存计划并推进到planned，不会直接执行修复；无安全修复方案时应升级人工。",
		func(_ context.Context, input submitPlanInput) (response[workflow.PlanSubmission], error) {
			capability, allowed := remediations[input.RemediationTool]
			if !allowed {
				return response[workflow.PlanSubmission]{}, fmt.Errorf("remediation tool %q is not available for this incident", input.RemediationTool)
			}
			if err := validateRemediationArguments(capability, input.RemediationArguments); err != nil {
				return response[workflow.PlanSubmission]{}, fmt.Errorf("validate planned remediation: %w", err)
			}
			for _, name := range capability.Arguments {
				if _, exists := input.RemediationArguments[name]; !exists {
					return response[workflow.PlanSubmission]{}, fmt.Errorf("planned remediation argument %q is required", name)
				}
			}
			if key, ok := input.RemediationArguments["idempotency_key"].(string); !ok || strings.TrimSpace(key) == "" {
				return response[workflow.PlanSubmission]{}, fmt.Errorf("planned remediation idempotency_key must be a non-empty string")
			}
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
