// Package tools 将平台能力包装为每个 Incident 独立的 Eino 工具集。
package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/workflow"
)

// Set 保存一个 Incident 可见的工具，不使用 Coding Agent 的全局 Registry。
type Set struct {
	tools     []einotool.BaseTool
	names     []string
	invokable map[string]einotool.InvokableTool
	actions   map[string]workflow.AgentAction
}

type options struct {
	workflow *workflow.IncidentWorkflow
}

// Option 配置 Incident 工具集的可选能力。
type Option func(*options)

// WithWorkflow 让工具集增加 submit_plan，并为工具记录 AgentAction 分类。
func WithWorkflow(value *workflow.IncidentWorkflow) Option {
	return func(target *options) { target.workflow = value }
}

// New 构建只绑定到指定 Incident 和 Platform 实例的安全工具集。
func New(ctx context.Context, service platform.ToolOpsPlatform, incidentID string, opts ...Option) (*Set, error) {
	if service == nil {
		return nil, fmt.Errorf("toolops platform is required")
	}
	incidentID = strings.TrimSpace(incidentID)
	if incidentID == "" {
		return nil, fmt.Errorf("incident ID is required")
	}
	config := options{}
	for _, option := range opts {
		if option != nil {
			option(&config)
		}
	}
	if config.workflow != nil && config.workflow.Snapshot().IncidentID != incidentID {
		return nil, fmt.Errorf("workflow incident ID does not match tool incident ID")
	}

	staticTools, err := buildStaticTools(service, incidentID)
	if err != nil {
		return nil, err
	}
	capabilities, err := service.GetRemediationCapabilities(ctx, platform.StateQuery{
		Scope: platform.Scope{IncidentID: incidentID},
	})
	if err != nil {
		return nil, fmt.Errorf("get remediation capabilities: %w", err)
	}
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i].Name < capabilities[j].Name })

	set := &Set{
		invokable: make(map[string]einotool.InvokableTool, len(staticTools)+len(capabilities)+1),
		actions:   make(map[string]workflow.AgentAction),
	}
	for _, item := range staticTools {
		if err := set.add(ctx, item); err != nil {
			return nil, err
		}
	}
	for _, capability := range capabilities {
		if err := set.add(ctx, newRemediationTool(service, incidentID, capability)); err != nil {
			return nil, err
		}
	}
	if config.workflow != nil {
		remediationCapabilities := make(map[string]platform.RemediationCapability, len(capabilities))
		for _, capability := range capabilities {
			remediationCapabilities[capability.Name] = capability
		}
		planTool, planErr := newSubmitPlanTool(config.workflow, remediationCapabilities)
		if planErr != nil {
			return nil, planErr
		}
		if err := set.add(ctx, planTool); err != nil {
			return nil, err
		}
	}
	return set, nil
}

func (s *Set) add(ctx context.Context, item einotool.InvokableTool) error {
	info, err := item.Info(ctx)
	if err != nil {
		return fmt.Errorf("read tool info: %w", err)
	}
	if info == nil || strings.TrimSpace(info.Name) == "" {
		return fmt.Errorf("tool name is required")
	}
	if strings.TrimSpace(info.Desc) == "" {
		return fmt.Errorf("tool %q description is required", info.Name)
	}
	if _, exists := s.invokable[info.Name]; exists {
		return fmt.Errorf("duplicate tool name %q", info.Name)
	}
	s.invokable[info.Name] = item
	s.tools = append(s.tools, item)
	s.names = append(s.names, info.Name)
	if action, ok := agentActionForTool(info.Name); ok {
		s.actions[info.Name] = action
	}
	return nil
}

// Tools 返回可直接传给 Eino ReAct Agent 或 ToolsNode 的工具列表副本。
func (s *Set) Tools() []einotool.BaseTool {
	if s == nil {
		return nil
	}
	return append([]einotool.BaseTool(nil), s.tools...)
}

// ToolsForActions 返回与当前 Workflow AgentAction 白名单匹配的工具。
// 没有 AgentAction 分类的修复、探测和恢复工具不会直接暴露给模型。
func (s *Set) ToolsForActions(actions []workflow.AgentAction) []einotool.BaseTool {
	if s == nil {
		return nil
	}
	allowed := make(map[workflow.AgentAction]struct{}, len(actions))
	for _, action := range actions {
		allowed[action] = struct{}{}
	}
	result := make([]einotool.BaseTool, 0, len(s.tools))
	for index, item := range s.tools {
		action, classified := s.actions[s.names[index]]
		if _, visible := allowed[action]; classified && visible {
			result = append(result, item)
		}
	}
	return result
}

// AgentAction 返回具体工具对应的 Workflow AgentAction。
func (s *Set) AgentAction(name string) (workflow.AgentAction, bool) {
	if s == nil {
		return "", false
	}
	action, ok := s.actions[name]
	return action, ok
}

// Resolve 根据模型调用使用的函数名取得可执行工具。
func (s *Set) Resolve(name string) (einotool.InvokableTool, bool) {
	if s == nil {
		return nil, false
	}
	item, ok := s.invokable[name]
	return item, ok
}

type response[T any] struct {
	Data  T      `json:"data"`
	Error string `json:"error,omitempty"`
}

func platformResponse[T any](data T, err error) response[T] {
	result := response[T]{Data: data}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

func agentActionForTool(name string) (workflow.AgentAction, bool) {
	switch name {
	case "query_metrics", "query_logs", "query_traces", "get_services", "get_routes", "get_providers",
		"get_config_versions", "get_change_records", "get_credential_metadata", "get_connection_metadata",
		"get_tasks", "get_remediation_capabilities":
		return workflow.AgentActionRead, true
	case "get_operation":
		return workflow.AgentActionQueryOperation, true
	case "submit_plan":
		return workflow.AgentActionSubmitPlan, true
	case "escalate_incident":
		return workflow.AgentActionEscalate, true
	default:
		return "", false
	}
}
