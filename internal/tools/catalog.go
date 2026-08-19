// Package tools 将平台能力包装为每个 Incident 独立的 Eino 工具集。
package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/skill"
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
	workflow     *workflow.IncidentWorkflow
	skillSession *skill.Session
	evidence     EvidenceReader
}

var discoveryAgentToolNames = []string{
	"get_evidence",
	"get_services",
	"get_routes",
	"get_providers",
	"query_metrics",
	"query_logs",
	"query_traces",
	"load_skill",
	"escalate_incident",
}

var activeSkillCoreToolNames = []string{
	"get_remediation_capabilities",
	"get_recovery_policies",
	"get_operation",
	"submit_execution_intent",
	"unload_skill",
}

// Option 配置 Incident 工具集的可选能力。
type Option func(*options)

// WithWorkflow 让工具集增加 submit_execution_intent，并为工具记录 AgentAction 分类。
func WithWorkflow(value *workflow.IncidentWorkflow) Option {
	return func(target *options) { target.workflow = value }
}

// WithSkillSession 为工具集增加由 Harness 控制的 Skill 渐进加载工具。
func WithSkillSession(value *skill.Session) Option {
	return func(target *options) { target.skillSession = value }
}

// WithEvidenceReader 为工具集绑定当前 Incident 的历史证据读取器。
func WithEvidenceReader(value EvidenceReader) Option {
	return func(target *options) { target.evidence = value }
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

	staticTools, err := buildStaticTools(service, incidentID, config.evidence)
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
	if config.skillSession != nil {
		skillTools, skillErr := buildSkillTools(config.skillSession)
		if skillErr != nil {
			return nil, skillErr
		}
		for _, item := range skillTools {
			if err := set.add(ctx, item); err != nil {
				return nil, err
			}
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
		intentTool, intentErr := newSubmitExecutionIntentTool(config.workflow, remediationCapabilities)
		if intentErr != nil {
			return nil, intentErr
		}
		if err := set.add(ctx, intentTool); err != nil {
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

// AgentToolNamesForSkills 返回渐进披露后的工具白名单。基础指标、日志、Trace
// 和状态查询在 Skill 加载前后始终可见；至少加载一个 Skill 后才增加领域查询、
// ExecutionIntent、恢复策略和卸载能力。
func AgentToolNamesForSkills(skillTools []string, hasActiveSkill bool) []string {
	capacity := len(discoveryAgentToolNames) + len(skillTools)
	if hasActiveSkill {
		capacity += len(activeSkillCoreToolNames)
	}
	result := make([]string, 0, capacity)
	seen := make(map[string]struct{}, cap(result))
	names := append([]string(nil), discoveryAgentToolNames...)
	if hasActiveSkill {
		names = append(names, activeSkillCoreToolNames...)
	}
	names = append(names, skillTools...)
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	return result
}

// ToolsForActionsAndNames 只返回同时通过 Workflow Action 和 Skill 工具白名单的工具。
// 白名单中的未知工具或非 Agent 工具属于部署配置错误，因此直接拒绝启动 Agent。
func (s *Set) ToolsForActionsAndNames(actions []workflow.AgentAction, names []string) ([]einotool.BaseTool, error) {
	if s == nil {
		return nil, fmt.Errorf("tool set is required")
	}
	allowedNames := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if _, exists := s.invokable[name]; !exists {
			return nil, fmt.Errorf("skill tool %q is not registered for this incident", name)
		}
		if _, classified := s.actions[name]; !classified {
			return nil, fmt.Errorf("skill tool %q is not available to the Agent workflow", name)
		}
		allowedNames[name] = struct{}{}
	}
	actionSet := make(map[workflow.AgentAction]struct{}, len(actions))
	for _, action := range actions {
		actionSet[action] = struct{}{}
	}
	result := make([]einotool.BaseTool, 0, len(names))
	for index, item := range s.tools {
		name := s.names[index]
		if _, allowed := allowedNames[name]; !allowed {
			continue
		}
		action, classified := s.actions[name]
		if _, visible := actionSet[action]; classified && visible {
			result = append(result, item)
		}
	}
	return result, nil
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
	Data        T        `json:"data"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
	Error       string   `json:"error,omitempty"`
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
	case "get_evidence":
		return workflow.AgentActionRecallEvidence, true
	case "query_metrics", "query_logs", "query_traces", "get_services", "get_routes", "get_providers",
		"get_config_versions", "get_change_records", "get_credential_metadata", "get_connection_metadata",
		"get_tasks", "get_remediation_capabilities", "get_recovery_policies":
		return workflow.AgentActionRead, true
	case "get_operation":
		return workflow.AgentActionQueryOperation, true
	case "load_skill", "unload_skill":
		return workflow.AgentActionManageSkill, true
	case "submit_execution_intent":
		return workflow.AgentActionSubmitExecutionIntent, true
	case "escalate_incident":
		return workflow.AgentActionEscalate, true
	default:
		return "", false
	}
}
