package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/wen/opentalon/internal/platform"
)

type remediationTool struct {
	service    platform.ToolOpsPlatform
	incidentID string
	capability platform.RemediationCapability
	info       *schema.ToolInfo
}

func newRemediationTool(service platform.ToolOpsPlatform, incidentID string, capability platform.RemediationCapability) einotool.InvokableTool {
	return &remediationTool{
		service: service, incidentID: incidentID, capability: capability,
		info: remediationToolInfo(capability),
	}
}

func (t *remediationTool) Info(context.Context) (*schema.ToolInfo, error) {
	return t.info, nil
}

func (t *remediationTool) InvokableRun(ctx context.Context, argumentsJSON string, _ ...einotool.Option) (string, error) {
	var arguments map[string]any
	if err := json.Unmarshal([]byte(argumentsJSON), &arguments); err != nil {
		return "", fmt.Errorf("decode remediation arguments: %w", err)
	}
	if err := validateRemediationArguments(t.capability, arguments); err != nil {
		return "", err
	}
	for _, name := range t.capability.Arguments {
		if _, ok := arguments[name]; !ok {
			return "", fmt.Errorf("required argument %q is missing", name)
		}
	}
	idempotencyKey, ok := arguments["idempotency_key"].(string)
	if !ok || strings.TrimSpace(idempotencyKey) == "" {
		return "", fmt.Errorf("idempotency_key must be a non-empty string")
	}
	expectedVersion, _ := arguments["expected_version"].(string)
	dryRun, _ := arguments["dry_run"].(bool)
	delete(arguments, "idempotency_key")
	delete(arguments, "expected_version")
	delete(arguments, "dry_run")

	operation, callErr := t.service.ExecuteRemediation(ctx, platform.RemediationRequest{
		IncidentID: t.incidentID, ToolName: t.capability.Name, Arguments: arguments,
		ExpectedVersion: expectedVersion, DryRun: dryRun, IdempotencyKey: idempotencyKey,
	})
	encoded, err := json.Marshal(platformResponse(operation, callErr))
	if err != nil {
		return "", fmt.Errorf("encode remediation result: %w", err)
	}
	return string(encoded), nil
}

func validateRemediationArguments(capability platform.RemediationCapability, arguments map[string]any) error {
	allowed := make(map[string]struct{}, len(capability.Arguments)+1)
	for _, name := range capability.Arguments {
		allowed[name] = struct{}{}
	}
	allowed["dry_run"] = struct{}{}
	for name, value := range arguments {
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("argument %q is not allowed for remediation %q", name, capability.Name)
		}
		switch name {
		case "expected_pool_generation":
			number, ok := value.(float64)
			if !ok || number < 0 || math.Trunc(number) != number {
				return fmt.Errorf("expected_pool_generation must be a non-negative integer")
			}
		case "dry_run":
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("dry_run must be a boolean")
			}
		default:
			text, ok := value.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return fmt.Errorf("%s must be a non-empty string", name)
			}
		}
	}
	return nil
}

func remediationToolInfo(capability platform.RemediationCapability) *schema.ToolInfo {
	params := make(map[string]*schema.ParameterInfo, len(capability.Arguments)+1)
	for _, name := range capability.Arguments {
		params[name] = remediationParameter(name, true)
	}
	if _, exists := params["dry_run"]; !exists {
		params["dry_run"] = remediationParameter("dry_run", false)
	}
	description := capability.Description
	description += fmt.Sprintf(" 风险等级：%s。", valueOrDefault(capability.Risk, "未标注"))
	if capability.RequiresApproval {
		description += "该操作需要平台审批，调用只表示提交受控操作。"
	}
	if len(capability.Preconditions) > 0 {
		keys := make([]string, 0, len(capability.Preconditions))
		for key := range capability.Preconditions {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		description += " 前置条件：" + strings.Join(keys, "、") + "。"
	}
	return &schema.ToolInfo{
		Name: capability.Name, Desc: description,
		Extra:       map[string]any{"risk": capability.Risk, "requires_approval": capability.RequiresApproval},
		ParamsOneOf: schema.NewParamsOneOfByParams(params),
	}
}

func remediationParameter(name string, required bool) *schema.ParameterInfo {
	result := &schema.ParameterInfo{Type: schema.String, Required: required, Desc: remediationArgumentDescription(name)}
	switch name {
	case "expected_pool_generation":
		result.Type = schema.Integer
	case "dry_run":
		result.Type = schema.Boolean
	}
	return result
}

func remediationArgumentDescription(name string) string {
	descriptions := map[string]string{
		"tool_id":                  "受影响的工具ID",
		"target_version":           "准备恢复到的已知健康版本",
		"expected_version":         "提交操作前观察到的当前配置版本，用于并发冲突检测",
		"provider_id":              "需要修复的Provider ID",
		"expected_pool_generation": "提交操作前观察到的连接池代次，用于并发冲突检测",
		"credential_id":            "凭证元数据ID，不是凭证值",
		"secret_reference":         "由安全平台管理的密钥引用，不能传入明文密钥",
		"idempotency_key":          "本次修复操作的唯一幂等键",
		"dry_run":                  "仅检查权限、参数和前置条件，不改变World状态",
	}
	if description := descriptions[name]; description != "" {
		return description
	}
	return "修复函数参数 " + name
}

func valueOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
