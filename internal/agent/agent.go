// Package agent 提供 Talon 的 ToolOps Agent 实现。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	flowagent "github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	"github.com/wen/opentalon/internal/observability"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/runartifact"
	"github.com/wen/opentalon/internal/skill"
	toolset "github.com/wen/opentalon/internal/tools"
	"github.com/wen/opentalon/internal/workflow"
)

const (
	// DefaultMaxSteps 限制一次 Agent 调用最多执行的 Eino Graph 节点数，
	// 防止模型在查询或工具失败时无限循环。
	DefaultMaxSteps = 24
	graphName       = "ToolOpsAgent"
)

// Config 定义一个绑定到单个 Incident 的 ToolOpsAgent。
// Model 和 Platform 由外层注入，因此 Agent 不依赖具体模型供应商或真实平台。
type Config struct {
	Model      model.ToolCallingChatModel
	Platform   platform.ToolOpsPlatform
	IncidentID string
	MaxSteps   int
	Workflow   *workflow.IncidentWorkflow
	Artifact   *runartifact.Recorder
	Skills     *skill.Session

	// AdditionalInstructions 只能补充当前部署环境的调查说明，
	// 固定的安全边界始终由 Agent 内置提示词和 Platform 共同保证。
	AdditionalInstructions string
}

// ToolOpsAgent 使用 Eino ReAct 循环调查 Incident，并调用受控的 ToolOps 工具。
// 每个实例只绑定一个 Incident，避免模型跨事件查询或执行动作。
type ToolOpsAgent struct {
	incidentID string
	systemText string
	tools      *toolset.Set
	workflow   *workflow.IncidentWorkflow
	skills     *skill.Session
	runner     *react.Agent
}

// NewToolOpsAgent 创建一个可嵌入后续 IncidentWorkflow 的单 Agent。
func NewToolOpsAgent(ctx context.Context, config Config) (*ToolOpsAgent, error) {
	if config.Model == nil {
		return nil, fmt.Errorf("tool calling model is required")
	}
	if config.Platform == nil {
		return nil, fmt.Errorf("toolops platform is required")
	}
	if config.Workflow == nil {
		return nil, fmt.Errorf("incident workflow is required")
	}
	incidentID := strings.TrimSpace(config.IncidentID)
	if incidentID == "" {
		return nil, fmt.Errorf("incident ID is required")
	}
	if config.MaxSteps < 0 {
		return nil, fmt.Errorf("max steps must not be negative")
	}
	maxSteps := config.MaxSteps
	if maxSteps == 0 {
		maxSteps = DefaultMaxSteps
	}

	toolOptions := []toolset.Option{toolset.WithWorkflow(config.Workflow)}
	if config.Skills != nil {
		toolOptions = append(toolOptions, toolset.WithSkillSession(config.Skills))
	}
	tools, err := toolset.New(ctx, config.Platform, incidentID, toolOptions...)
	if err != nil {
		return nil, fmt.Errorf("build incident tools: %w", err)
	}

	visibleTools, err := visibleAgentTools(tools, config.Workflow, config.Skills)
	if err != nil {
		return nil, err
	}
	persona := systemPrompt(incidentID, config.AdditionalInstructions)
	if config.Skills != nil {
		catalog, marshalErr := json.Marshal(config.Skills.Catalog())
		if marshalErr != nil {
			return nil, fmt.Errorf("encode Skill Catalog: %w", marshalErr)
		}
		persona += "\n\n可安装 Skill Catalog（仅元数据，正文尚未加载）：\n" + string(catalog)
	}
	trackedModel := model.ToolCallingChatModel(config.Model)
	if config.Artifact != nil {
		trackedModel = &recordingModel{next: config.Model, recorder: config.Artifact}
	}
	runner, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: trackedModel,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools:               visibleTools,
			ExecuteSequentially: true,
			ToolCallMiddlewares: []compose.ToolMiddleware{workflowToolGuard(config.Workflow, tools, config.Skills, config.Artifact)},
			UnknownToolsHandler: func(_ context.Context, name, input string) (string, error) {
				result, marshalErr := json.Marshal(map[string]any{
					"data":  nil,
					"error": fmt.Sprintf("工具 %s 不存在，只能调用当前已注册的工具", name),
				})
				if config.Artifact != nil {
					config.Artifact.RecordUnknownTool(name, input, string(result), marshalErr)
				}
				return string(result), marshalErr
			},
		},
		MaxStep:       maxSteps,
		GraphName:     graphName,
		ModelNodeName: "ToolOpsModel",
		ToolsNodeName: "ToolOpsTools",
	})
	if err != nil {
		return nil, fmt.Errorf("build ToolOps ReAct agent: %w", err)
	}

	return &ToolOpsAgent{
		incidentID: incidentID, systemText: persona, tools: tools,
		workflow: config.Workflow, skills: config.Skills, runner: runner,
	}, nil
}

// IncidentID 返回当前 Agent 被授权处理的唯一 Incident。
func (a *ToolOpsAgent) IncidentID() string {
	if a == nil {
		return ""
	}
	return a.incidentID
}

// Investigate 实现顶层 IncidentController 所需的最小调查接口。
// Controller 只依赖 Agent 是否通过工具推动了 Workflow，不解析自然语言回答。
func (a *ToolOpsAgent) Investigate(ctx context.Context, instruction string) error {
	_, err := a.Run(ctx, instruction)
	return err
}

// Run 使用一条工作流指令启动一次 Agent 调用。
// instruction 为空时，Agent 默认开始调查当前 Incident。
func (a *ToolOpsAgent) Run(ctx context.Context, instruction string, opts ...flowagent.AgentOption) (*schema.Message, error) {
	if a == nil || a.runner == nil {
		return nil, fmt.Errorf("ToolOpsAgent is not initialized")
	}
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		instruction = defaultInstruction
	}
	messages := []*schema.Message{
		schema.SystemMessage(a.currentSystemText()),
		schema.UserMessage(instruction),
	}
	return a.generate(ctx, "run", messages, opts...)
}

// Generate 使用已有消息继续一次 Agent 调用，供后续工作流恢复上下文时使用。
func (a *ToolOpsAgent) Generate(ctx context.Context, messages []*schema.Message, opts ...flowagent.AgentOption) (*schema.Message, error) {
	if a == nil || a.runner == nil {
		return nil, fmt.Errorf("ToolOpsAgent is not initialized")
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("agent messages are required")
	}
	return a.generate(ctx, "generate", a.withSystemMessage(messages), opts...)
}

func (a *ToolOpsAgent) generate(ctx context.Context, operation string, messages []*schema.Message, opts ...flowagent.AgentOption) (result *schema.Message, err error) {
	initial := a.workflow.Snapshot()
	ctx, run := observability.StartAgentRun(ctx, a.incidentID, operation, string(initial.State), messages)
	defer func() {
		final := a.workflow.Snapshot()
		observability.FinishAgentRun(run, err, string(final.State), workflowTransitionsAfter(final.History, initial.Version), result)
	}()

	options, err := a.withWorkflowTools(ctx, opts)
	if err != nil {
		return nil, err
	}
	return a.runner.Generate(ctx, messages, options...)
}

func workflowTransitionsAfter(history []workflow.Transition, version uint64) []observability.WorkflowTransition {
	result := make([]observability.WorkflowTransition, 0)
	for _, transition := range history {
		if transition.Version <= version {
			continue
		}
		result = append(result, observability.WorkflowTransition{
			From: string(transition.From), To: string(transition.To), Event: string(transition.Event),
			Actor: string(transition.Actor), Version: transition.Version, At: transition.At,
		})
	}
	return result
}

// Stream 以流式方式运行 Agent，主要用于命令行或管理界面展示。
func (a *ToolOpsAgent) Stream(ctx context.Context, messages []*schema.Message, opts ...flowagent.AgentOption) (*schema.StreamReader[*schema.Message], error) {
	if a == nil || a.runner == nil {
		return nil, fmt.Errorf("ToolOpsAgent is not initialized")
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("agent messages are required")
	}
	messages = a.withSystemMessage(messages)
	initial := a.workflow.Snapshot()
	ctx, run := observability.StartAgentRun(ctx, a.incidentID, "stream", string(initial.State), messages)
	options, err := a.withWorkflowTools(ctx, opts)
	if err != nil {
		observability.FinishAgentRun(run, err, string(initial.State), nil, nil)
		return nil, err
	}
	source, err := a.runner.Stream(ctx, messages, options...)
	if err != nil {
		final := a.workflow.Snapshot()
		observability.FinishAgentRun(run, err, string(final.State), workflowTransitionsAfter(final.History, initial.Version), nil)
		return nil, err
	}
	if run == nil {
		return source, nil
	}

	// 用一个透传 Stream 把根 Span 的结束时间绑定到实际消费完成，而不是 Stream 方法返回时。
	result, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer writer.Close()
		defer source.Close()
		var chunks []*schema.Message
		var streamErr error
		defer func() {
			final := a.workflow.Snapshot()
			observability.FinishAgentRun(run, streamErr, string(final.State), workflowTransitionsAfter(final.History, initial.Version), chunks)
		}()
		for {
			chunk, recvErr := source.Recv()
			if recvErr != nil {
				if !errors.Is(recvErr, io.EOF) {
					streamErr = recvErr
					writer.Send(nil, recvErr)
				}
				return
			}
			chunks = append(chunks, chunk)
			if writer.Send(chunk, nil) {
				return
			}
		}
	}()
	return result, nil
}

func (a *ToolOpsAgent) withWorkflowTools(ctx context.Context, options []flowagent.AgentOption) ([]flowagent.AgentOption, error) {
	visible, err := visibleAgentTools(a.tools, a.workflow, a.skills)
	if err != nil {
		return nil, err
	}
	policyOptions, err := react.WithTools(ctx, visible...)
	if err != nil {
		return nil, fmt.Errorf("build workflow tool options: %w", err)
	}
	result := make([]flowagent.AgentOption, 0, len(options)+len(policyOptions)+1)
	result = append(result, options...)
	if handler := observability.EinoHandler(); handler != nil {
		result = append(result, flowagent.WithComposeOptions(compose.WithCallbacks(handler)))
	}
	return append(result, policyOptions...), nil
}

func visibleAgentTools(tools *toolset.Set, instance *workflow.IncidentWorkflow, skills *skill.Session) ([]einotool.BaseTool, error) {
	if skills == nil {
		return tools.ToolsForActions(instance.AllowedAgentActions()), nil
	}
	names := activeSkillToolNames(skills)
	visible, err := tools.ToolsForActionsAndNames(instance.AllowedAgentActions(), names)
	if err != nil {
		return nil, fmt.Errorf("apply active Skill tool policy: %w", err)
	}
	return visible, nil
}

func activeSkillToolNames(session *skill.Session) []string {
	active := session.Active()
	specific := make([]string, 0)
	for _, definition := range active {
		specific = append(specific, definition.AllowedTools...)
	}
	return toolset.AgentToolNamesForSkills(specific, len(active) > 0)
}

func skillPolicyAllowsTool(session *skill.Session, name string) bool {
	for _, allowed := range activeSkillToolNames(session) {
		if allowed == name {
			return true
		}
	}
	return false
}

// workflowToolGuard 在每次工具真正执行前重新读取 Workflow 状态并校验 AgentAction。
// 模型在一轮 ReAct 开始时拿到的工具列表可能因 submit_plan 或 escalate 等调用而过期，
// 因此不能只依赖“模型是否看得见工具”；未分类或当前状态已禁止的工具会返回结构化拒绝结果。
// escalate_incident 成功后，Guard 还负责提交 EventEscalated，使平台操作和状态机保持一致。
// load_skill、unload_skill、submit_plan 或 escalate_incident 成功推进到目标状态后，
// Guard 直接结束当前 ReAct，避免在同一轮使用过期工具集或额外消耗 Graph 步数。
func workflowToolGuard(instance *workflow.IncidentWorkflow, tools *toolset.Set, skills *skill.Session, recorder *runartifact.Recorder) compose.ToolMiddleware {
	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				started := time.Now()
				action, classified := tools.AgentAction(input.Name)
				if skills != nil {
					if !skillPolicyAllowsTool(skills, input.Name) {
						denied, err := deniedToolOutput(fmt.Errorf("tool %q is not allowed by the active Skill", input.Name))
						recordToolCall(recorder, input, action, denied, started, err, true)
						return denied, err
					}
				}
				if !classified {
					denied, err := deniedToolOutput(fmt.Errorf("tool %q is not available to the Agent workflow", input.Name))
					recordToolCall(recorder, input, action, denied, started, err, true)
					return denied, err
				}
				if err := instance.AuthorizeAgentAction(action); err != nil {
					denied, outputErr := deniedToolOutput(err)
					recordToolCall(recorder, input, action, denied, started, outputErr, true)
					return denied, outputErr
				}
				output, err := next(ctx, input)
				if err != nil {
					recordToolCall(recorder, input, action, output, started, err, false)
					return nil, err
				}
				if action == workflow.AgentActionRead && input.CallID != "" && toolResponseSucceeded(output.Result) {
					if attachErr := attachEvidenceReference(output, input.CallID); attachErr != nil {
						recordToolCall(recorder, input, action, output, started, attachErr, false)
						return nil, attachErr
					}
				}
				if action == workflow.AgentActionEscalate && toolResponseSucceeded(output.Result) {
					if _, applyErr := instance.Apply(workflow.Event{Type: workflow.EventEscalated, Actor: workflow.ActorAgent}); applyErr != nil {
						recordToolCall(recorder, input, action, output, started, applyErr, false)
						return nil, fmt.Errorf("apply escalation event: %w", applyErr)
					}
				}
				if action == workflow.AgentActionManageSkill && toolResponseSucceeded(output.Result) {
					if applyErr := applySkillChange(ctx, instance, input.Name, output.Result); applyErr != nil {
						recordToolCall(recorder, input, action, output, started, applyErr, false)
						return nil, applyErr
					}
				}
				if returnErr := returnAfterTerminalAction(ctx, instance, action, output); returnErr != nil {
					recordToolCall(recorder, input, action, output, started, returnErr, false)
					return nil, returnErr
				}
				recordToolCall(recorder, input, action, output, started, nil, false)
				return output, nil
			}
		},
	}
}

func returnAfterTerminalAction(ctx context.Context, instance *workflow.IncidentWorkflow, action workflow.AgentAction, output *compose.ToolOutput) error {
	if output == nil || !toolResponseSucceeded(output.Result) {
		return nil
	}
	state := instance.Snapshot().State
	if (action == workflow.AgentActionSubmitPlan && state != workflow.StatePlanned) ||
		(action == workflow.AgentActionEscalate && state != workflow.StateEscalated) {
		return nil
	}
	if action != workflow.AgentActionSubmitPlan && action != workflow.AgentActionEscalate {
		return nil
	}
	if err := react.SetReturnDirectly(ctx); err != nil {
		return fmt.Errorf("stop ReAct after successful %s: %w", action, err)
	}
	return nil
}

func applySkillChange(ctx context.Context, instance *workflow.IncidentWorkflow, toolName, result string) error {
	var decoded struct {
		Data skill.Change `json:"data"`
	}
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		return fmt.Errorf("decode %s result: %w", toolName, err)
	}
	eventType := workflow.EventSkillLoaded
	expectedAction := "loaded"
	if toolName == "unload_skill" {
		eventType = workflow.EventSkillUnloaded
		expectedAction = "unloaded"
	} else if toolName != "load_skill" {
		return fmt.Errorf("unknown Skill control tool %q", toolName)
	}
	if decoded.Data.Action != expectedAction || strings.TrimSpace(decoded.Data.Name) == "" {
		return fmt.Errorf("%s returned an invalid Skill change", toolName)
	}
	_, err := instance.Apply(workflow.Event{
		Type: eventType, Actor: workflow.ActorAgent, Reason: decoded.Data.Reason,
		Metadata: map[string]string{
			"skill_name":    decoded.Data.Name,
			"skill_digest":  decoded.Data.Digest,
			"evidence_refs": strings.Join(decoded.Data.EvidenceRefs, ","),
		},
	})
	if err != nil {
		return fmt.Errorf("record %s workflow event: %w", toolName, err)
	}
	if err := react.SetReturnDirectly(ctx); err != nil {
		return fmt.Errorf("stop ReAct after successful %s: %w", toolName, err)
	}
	return nil
}

func recordToolCall(recorder *runartifact.Recorder, input *compose.ToolInput, action workflow.AgentAction, output *compose.ToolOutput, started time.Time, err error, denied bool) {
	if recorder == nil || input == nil {
		return
	}
	result := ""
	if output != nil {
		result = output.Result
	}
	recorder.RecordToolCall(input.CallID, input.Name, action, input.Arguments, result, started, err, denied)
}

func deniedToolOutput(err error) (*compose.ToolOutput, error) {
	result, marshalErr := json.Marshal(map[string]any{"data": nil, "error": err.Error()})
	if marshalErr != nil {
		return nil, marshalErr
	}
	return &compose.ToolOutput{Result: string(result)}, nil
}

func toolResponseSucceeded(value string) bool {
	var result struct {
		Error string `json:"error"`
	}
	return json.Unmarshal([]byte(value), &result) == nil && result.Error == ""
}

// attachEvidenceReference 把 Harness 信任的工具调用 ID 显式放入模型可见的只读结果。
// 后续 load_skill/unload_skill 必须原样复制该值，避免模型猜测内部 Artifact 引用。
func attachEvidenceReference(output *compose.ToolOutput, ref string) error {
	if output == nil {
		return errors.New("attach evidence reference: nil tool output")
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal([]byte(output.Result), &result); err != nil {
		return fmt.Errorf("attach evidence reference: decode tool result: %w", err)
	}
	if result == nil {
		return errors.New("attach evidence reference: tool result must be a JSON object")
	}
	encodedRef, err := json.Marshal(ref)
	if err != nil {
		return fmt.Errorf("attach evidence reference: encode reference: %w", err)
	}
	result["evidence_ref"] = encodedRef
	encodedResult, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("attach evidence reference: encode tool result: %w", err)
	}
	output.Result = string(encodedResult)
	return nil
}

// withSystemMessage 按照 Eino ReAct 的推荐方式，在调用 Generate 或 Stream 前
// 直接把 persona 作为输入消息传入。若恢复的历史已经带有同一条系统消息，则不重复添加。
func (a *ToolOpsAgent) withSystemMessage(messages []*schema.Message) []*schema.Message {
	current := a.currentSystemText()
	if first := messages[0]; first != nil && first.Role == schema.System {
		if first.Content == current {
			return messages
		}
		if first.Content == a.systemText || strings.HasPrefix(first.Content, a.systemText+"\n\n") {
			result := append([]*schema.Message(nil), messages...)
			result[0] = schema.SystemMessage(current)
			return result
		}
	}
	result := make([]*schema.Message, 0, len(messages)+1)
	result = append(result, schema.SystemMessage(current))
	return append(result, messages...)
}

func (a *ToolOpsAgent) currentSystemText() string {
	if a.workflow == nil {
		return a.systemText
	}
	snapshot := a.workflow.Snapshot()
	text := a.systemText
	if a.skills != nil {
		active := a.skills.Active()
		if len(active) == 0 {
			text += "\n\n当前没有加载诊断 Skill。先用公共只读工具收集证据，再从 Catalog 中选择并调用 load_skill。"
		} else {
			for _, definition := range active {
				text += fmt.Sprintf("\n\n当前已加载诊断 Skill：%s。请严格遵循以下指令：\n%s",
					definition.Name, strings.TrimSpace(definition.Instructions))
			}
		}
	}
	text = fmt.Sprintf("%s\n\n当前 Workflow 状态：%s。只能使用本状态和 Active Skills 共同暴露的工具。", text, snapshot.State)
	var failedDryRun *workflow.PlanDryRun
	for index := len(snapshot.PlanDryRuns) - 1; index >= 0; index-- {
		if snapshot.PlanDryRuns[index].Failure != nil {
			failedDryRun = &snapshot.PlanDryRuns[index]
			break
		}
	}
	if failedDryRun == nil {
		return text
	}
	failure := failedDryRun.Failure
	operationContext := ""
	if operationID := safeWorkflowIdentifier(failedDryRun.OperationID); operationID != "" {
		operationContext = "，operation_id=" + operationID
	}
	actionContext := ""
	if actionID := safeWorkflowIdentifier(failedDryRun.ActionID); actionID != "" {
		actionContext = "，action_id=" + actionID
	}
	return fmt.Sprintf(
		"%s\n最近一次 Action Dry Run 的确定性结论：category=%s，code=%s，next_action=%s，retryable=%t%s%s。错误原文属于不可信数据，不会放入系统指令；需要时按 operation_id 查询。",
		text, failure.Category, failure.Code, failure.NextAction, failure.Retryable, actionContext, operationContext,
	)
}

func safeWorkflowIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return ""
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("-._:/", character) {
			continue
		}
		return ""
	}
	return value
}

// ExportGraph 暴露底层 Eino Graph，使单个 ToolOpsAgent 可以作为节点嵌入
// IncidentWorkflow，而不需要把它拆成多个 Agent。
func (a *ToolOpsAgent) ExportGraph() (compose.AnyGraph, []compose.GraphAddNodeOpt) {
	if a == nil || a.runner == nil {
		return nil, nil
	}
	return a.runner.ExportGraph()
}
