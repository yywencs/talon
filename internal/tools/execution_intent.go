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

// submitExecutionIntentInput 是模型调用 submit_execution_intent 时必须提供的结构化参数。
// 它用线性短 Stage 描述可验证动作和阶段间数据依赖。
type submitExecutionIntentActionInput struct {
	ID                 string                                    `json:"id,omitempty" jsonschema:"description=Stage 间引用使用的稳定动作标识"`
	ToolName           string                                    `json:"tool_name" jsonschema:"required,description=准备执行的已注册 remediation 名称或 request_probe、request_recovery；request_probe/request_recovery 的参数名固定为 route_id、policy_id、idempotency_key"`
	Arguments          map[string]any                            `json:"arguments" jsonschema:"required,description=当前已经确定的动作参数；probe/recovery 必须使用 policy_id，不要使用 recovery_policy_id"`
	ArgumentReferences map[string]workflow.ActionOutputReference `json:"argument_references,omitempty" jsonschema:"description=参数名到前序 Action 结构化输出的类型化引用"`
}

type submitExecutionStageInput struct {
	StageID          string                             `json:"stage_id" jsonschema:"required,description=线性 Stage 的稳定标识"`
	Goal             string                             `json:"goal" jsonschema:"required,description=当前阶段的单一目标"`
	Actions          []submitExecutionIntentActionInput `json:"actions" jsonschema:"required,description=本阶段按顺序执行的动作"`
	SuccessCriteria  []string                           `json:"success_criteria,omitempty"`
	CheckpointPolicy workflow.CheckpointPolicy          `json:"checkpoint_policy" jsonschema:"required,description=确定性阶段检查规则；request_probe Stage 仅允许 output.outcome=healthy 时 continue 到显式 recovery Stage，不能直接 succeeded，并使用 fail-closed 默认决策"`
}

type submitExecutionIntentInput struct {
	Summary      string                      `json:"summary" jsonschema:"required,description=本次有界执行意图的摘要和预期结果"`
	RootCause    string                      `json:"root_cause" jsonschema:"required,description=由证据支持的根因判断"`
	EvidenceRefs []string                    `json:"evidence_refs" jsonschema:"required,description=支持根因和修复选择的证据引用"`
	Stages       []submitExecutionStageInput `json:"stages" jsonschema:"required,description=按执行顺序排列的线性短 Stage"`
}

// newSubmitExecutionIntentTool 创建提交有界执行意图的 Eino 工具。
// remediations 是当前 Incident 允许使用的修复能力快照；工具只负责校验并冻结当前意图，
// 不会在提交过程中执行修复。提交成功后，Workflow 会从 investigating 转为 validating。
func newSubmitExecutionIntentTool(instance *workflow.IncidentWorkflow, remediations map[string]platform.RemediationCapability) (einotool.InvokableTool, error) {
	tool, err := toolutils.InferTool(
		"submit_execution_intent",
		"证据足够后提交当前有界执行意图。优先只提交当前可确定的短 Stage；仅当后续动作已经确定且只依赖前序结构化输出时，才可附带紧邻 Stage。Stage action 可使用已注册 remediation、request_probe 或 request_recovery。request_probe 健康只能 continue 到显式 request_recovery Stage，不能直接 succeeded。request_probe 和 request_recovery 的 arguments 必须是 route_id、policy_id、idempotency_key（策略字段名是 policy_id，不是 recovery_policy_id）。该工具只冻结意图并推进到 validating，不会直接执行动作；无安全方案时应升级人工。",
		func(_ context.Context, input submitExecutionIntentInput) (response[workflow.ExecutionIntentSubmission], error) {
			if len(input.Stages) == 0 {
				return platformResponse(workflow.ExecutionIntentSubmission{}, fmt.Errorf("intent stages is required")), nil
			}
			convertActions := func(values []submitExecutionIntentActionInput) ([]workflow.IntendedAction, error) {
				actions := make([]workflow.IntendedAction, 0, len(values))
				for index, action := range values {
					arguments, references, normalizeErr := normalizeManagedActionArguments(action.ToolName, action.Arguments, action.ArgumentReferences)
					if normalizeErr != nil {
						return nil, fmt.Errorf("validating action %d: %w", index, normalizeErr)
					}
					action.Arguments, action.ArgumentReferences = arguments, references
					capability, allowed := remediations[action.ToolName]
					kind := workflow.ActionKindRemediation
					if !allowed {
						switch action.ToolName {
						case "request_probe":
							kind = workflow.ActionKindProbe
						case "request_recovery":
							kind = workflow.ActionKindRecovery
						default:
							return nil, fmt.Errorf("action %d capability %q is not available for this incident", index, action.ToolName)
						}
						capability = platform.RemediationCapability{Name: action.ToolName, Risk: "low", Arguments: []string{"route_id", "policy_id", "idempotency_key"}}
					}
					if err := validateRemediationArguments(capability, action.Arguments); err != nil {
						return nil, fmt.Errorf("validate action %d: %w", index, err)
					}
					allowedArguments := make(map[string]struct{}, len(capability.Arguments))
					for _, name := range capability.Arguments {
						allowedArguments[name] = struct{}{}
					}
					for name, reference := range action.ArgumentReferences {
						if _, allowed := allowedArguments[name]; !allowed {
							return nil, fmt.Errorf("validating action %d reference target %q is not allowed for remediation %q", index, name, capability.Name)
						}
						expected := workflow.ActionOutputString
						if name == "expected_pool_generation" {
							expected = workflow.ActionOutputInteger
						}
						if reference.ExpectedType != expected {
							return nil, fmt.Errorf("validating action %d reference target %q requires expected_type %q", index, name, expected)
						}
					}
					for _, name := range capability.Arguments {
						_, literal := action.Arguments[name]
						reference, referenced := action.ArgumentReferences[name]
						if !literal && !referenced {
							return nil, fmt.Errorf("validating action %d argument %q is required", index, name)
						}
						if referenced && !reference.Required {
							return nil, fmt.Errorf("validating action %d required argument %q must use a required output reference", index, name)
						}
					}
					// 每个 Action 使用独立幂等键，避免部分重试时重复执行其他动作。
					if key, ok := action.Arguments["idempotency_key"].(string); !ok || strings.TrimSpace(key) == "" {
						return nil, fmt.Errorf("validating action %d idempotency_key must be a non-empty string", index)
					}
					actions = append(actions, workflow.IntendedAction{ID: action.ID, Key: action.ID, Kind: kind, ToolName: action.ToolName,
						Arguments: action.Arguments, ArgumentReferences: action.ArgumentReferences})
				}
				return actions, nil
			}
			stages := make([]workflow.ExecutionStageDraft, 0, len(input.Stages))
			for _, stage := range input.Stages {
				stageActions, stageErr := convertActions(stage.Actions)
				if stageErr != nil {
					return platformResponse(workflow.ExecutionIntentSubmission{}, fmt.Errorf("stage %q: %w", stage.StageID, stageErr)), nil
				}
				stages = append(stages, workflow.ExecutionStageDraft{StageID: stage.StageID, Goal: stage.Goal,
					Actions: stageActions, SuccessCriteria: stage.SuccessCriteria, CheckpointPolicy: stage.CheckpointPolicy,
					CreatedBy: string(workflow.ActorAgent)})
			}
			// SubmitExecutionIntent 会再次校验 Workflow 权限，并原子地冻结意图和推进状态。
			result, submitErr := instance.SubmitExecutionIntent(workflow.ExecutionIntentDraft{
				Summary: input.Summary, RootCause: input.RootCause, EvidenceRefs: input.EvidenceRefs,
				Stages: stages,
			})
			return platformResponse(result, submitErr), nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("build submit_execution_intent tool: %w", err)
	}
	return tool, nil
}

// normalizeManagedActionArguments 接受一次受控的旧字段别名，并立即规范化为平台协议中的 policy_id。
// 归一化只发生在 Stage Action 内，不恢复已经删除的顶层 recovery_policy_id ExecutionIntent 字段。
func normalizeManagedActionArguments(toolName string, arguments map[string]any, references map[string]workflow.ActionOutputReference) (map[string]any, map[string]workflow.ActionOutputReference, error) {
	resultArguments := make(map[string]any, len(arguments))
	for name, value := range arguments {
		resultArguments[name] = value
	}
	resultReferences := make(map[string]workflow.ActionOutputReference, len(references))
	for name, value := range references {
		resultReferences[name] = value
	}
	if toolName != "request_probe" && toolName != "request_recovery" {
		return resultArguments, resultReferences, nil
	}
	if alias, exists := resultArguments["recovery_policy_id"]; exists {
		if _, canonical := resultArguments["policy_id"]; canonical {
			return nil, nil, fmt.Errorf("arguments cannot contain both policy_id and recovery_policy_id")
		}
		resultArguments["policy_id"] = alias
		delete(resultArguments, "recovery_policy_id")
	}
	if alias, exists := resultReferences["recovery_policy_id"]; exists {
		if _, canonical := resultReferences["policy_id"]; canonical {
			return nil, nil, fmt.Errorf("argument_references cannot contain both policy_id and recovery_policy_id")
		}
		resultReferences["policy_id"] = alias
		delete(resultReferences, "recovery_policy_id")
	}
	return resultArguments, resultReferences, nil
}
