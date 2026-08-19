package workflow

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalidTransition = errors.New("invalid incident transition")
	ErrActorNotAllowed   = errors.New("incident event actor not allowed")
)

// Config 定义新建或从 checkpoint 恢复的 IncidentWorkflow。
type Config struct {
	IncidentID     string
	IntentIDPrefix string
	InitialState   State
	Now            func() time.Time
	Limits         ExecutionLimits
}

// Transition 是一次已经通过校验并提交的状态变化审计记录。
type Transition struct {
	Version  uint64            `json:"version"`
	From     State             `json:"from"`
	To       State             `json:"to"`
	Event    EventType         `json:"event"`
	Actor    Actor             `json:"actor"`
	Reason   string            `json:"reason,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Failure  *StageFailure     `json:"failure,omitempty"`
	At       time.Time         `json:"at"`
}

// Snapshot 是可用于审计和后续 checkpoint 的 Workflow 只读副本。
type Snapshot struct {
	IncidentID         string                 `json:"incident_id"`
	State              State                  `json:"state"`
	SuspendedState     State                  `json:"suspended_state,omitempty"`
	Version            uint64                 `json:"version"`
	ExecutionIntent    *ExecutionIntent       `json:"execution_intent,omitempty"`
	ExecutionIntents   []ExecutionIntent      `json:"execution_intents"`
	ActionDryRuns      []ActionDryRun         `json:"action_dry_runs,omitempty"`
	ActionPolicies     []ActionPolicyDecision `json:"action_policies,omitempty"`
	ActionApprovals    []ActionApproval       `json:"action_approvals,omitempty"`
	AllActionDryRuns   []ActionDryRun         `json:"all_action_dry_runs,omitempty"`
	AllActionPolicies  []ActionPolicyDecision `json:"all_action_policies,omitempty"`
	AllActionApprovals []ActionApproval       `json:"all_action_approvals,omitempty"`
	ActiveStageIndex   int                    `json:"active_stage_index"`
	ResolvedActions    []ResolvedAction       `json:"resolved_actions,omitempty"`
	ActionResults      []ActionResult         `json:"action_results,omitempty"`
	Checkpoints        []DecisionCheckpoint   `json:"checkpoints,omitempty"`
	Limits             ExecutionLimits        `json:"limits"`
	StagesExecuted     int                    `json:"stages_executed"`
	AgentResumesUsed   int                    `json:"agent_resumes_used"`
	ActionsAccepted    int                    `json:"actions_accepted"`
	Failures           []StageFailure         `json:"failures,omitempty"`
	History            []Transition           `json:"history"`
}

// IncidentWorkflow 保存单个 Incident 的确定性生命周期状态。
// 它不执行 LLM 或平台操作，只负责判断“是否允许”和“下一状态是什么”。
type IncidentWorkflow struct {
	mu sync.RWMutex

	incidentID         string
	intentIDPrefix     string
	state              State
	suspendedState     State
	version            uint64
	intent             *ExecutionIntent
	executionIntents   []ExecutionIntent
	actionDryRuns      []ActionDryRun
	actionPolicies     []ActionPolicyDecision
	actionApprovals    []ActionApproval
	allActionDryRuns   []ActionDryRun
	allActionPolicies  []ActionPolicyDecision
	allActionApprovals []ActionApproval
	activeStageIndex   int
	resolvedActions    []ResolvedAction
	actionResults      []ActionResult
	checkpoints        []DecisionCheckpoint
	limits             ExecutionLimits
	stagesExecuted     int
	agentResumesUsed   int
	actionsAccepted    int
	failures           []StageFailure
	history            []Transition
	now                func() time.Time
}

// transitionRule 定义一条状态转换的目标和事件来源权限。
// to 是事件发生后进入的目标状态；actors 是允许发出该事件的角色白名单，
// 不代表多个目标状态。
type transitionRule struct {
	to     State
	actors actorSet
}

// transitionRules 是 IncidentWorkflow 的确定性状态转换表。
// 外层 key 是当前状态，内层 key 是当前状态允许接收的事件，value 描述
// 该事件可以由哪些 Actor 发出，以及校验通过后应流转到哪个状态。
// 同一当前状态可以通过不同事件到达不同状态，但一条规则只对应一个目标状态。
var transitionRules = map[State]map[EventType]transitionRule{
	StateProtected: {
		EventStartInvestigation: {to: StateInvestigating, actors: actors(ActorController)},
	},
	StateInvestigating: {
		EventExecutionIntentSubmitted: {to: StateValidating, actors: actors(ActorAgent)},
		EventSkillLoaded:              {to: StateInvestigating, actors: actors(ActorAgent)},
		EventSkillUnloaded:            {to: StateInvestigating, actors: actors(ActorAgent)},
	},
	StateValidating: {
		EventExecutionAuthorized:     {to: StateExecuting, actors: actors(ActorWorkflow)},
		EventApprovalRequired:        {to: StateAwaitingApproval, actors: actors(ActorWorkflow)},
		EventExecutionIntentRejected: {to: StateInvestigating, actors: actors(ActorWorkflow, ActorHuman)},
		EventCheckpointFailed:        {to: StateFailed, actors: actors(ActorWorkflow)},
		EventCheckpointBlocked:       {to: StateBlocked, actors: actors(ActorWorkflow)},
		EventCheckpointEscalated:     {to: StateEscalated, actors: actors(ActorWorkflow)},
		EventCheckpointNeedsAgent:    {to: StateInvestigating, actors: actors(ActorWorkflow)},
	},
	StateAwaitingApproval: {
		EventExecutionAuthorized:     {to: StateExecuting, actors: actors(ActorHuman)},
		EventExecutionIntentRejected: {to: StateInvestigating, actors: actors(ActorHuman)},
	},
	StateExecuting: {
		EventStageCheckpoint:      {to: StateCheckpoint, actors: actors(ActorWorkflow)},
		EventCheckpointNeedsAgent: {to: StateInvestigating, actors: actors(ActorWorkflow)},
		EventCheckpointFailed:     {to: StateFailed, actors: actors(ActorWorkflow)},
		EventCheckpointEscalated:  {to: StateEscalated, actors: actors(ActorWorkflow)},
		EventCheckpointBlocked:    {to: StateBlocked, actors: actors(ActorWorkflow)},
	},
	StateCheckpoint: {
		EventCheckpointContinue:   {to: StateValidating, actors: actors(ActorWorkflow)},
		EventCheckpointNeedsAgent: {to: StateInvestigating, actors: actors(ActorWorkflow)},
		EventCheckpointSucceeded:  {to: StateResolved, actors: actors(ActorWorkflow)},
		EventCheckpointFailed:     {to: StateFailed, actors: actors(ActorWorkflow)},
		EventCheckpointEscalated:  {to: StateEscalated, actors: actors(ActorWorkflow)},
		EventCheckpointBlocked:    {to: StateBlocked, actors: actors(ActorWorkflow)},
	},
	StateEscalated: {
		EventHumanResumed: {to: StateInvestigating, actors: actors(ActorHuman)},
	},
}

// NewIncidentWorkflow 创建一个状态机。InitialState 为空时从 protected 开始。
func NewIncidentWorkflow(config Config) (*IncidentWorkflow, error) {
	incidentID := strings.TrimSpace(config.IncidentID)
	if incidentID == "" {
		return nil, fmt.Errorf("incident ID is required")
	}
	intentIDPrefix := strings.TrimSpace(config.IntentIDPrefix)
	if intentIDPrefix == "" {
		intentIDPrefix = incidentID
	}
	state := config.InitialState
	if state == "" {
		state = StateProtected
	}
	if err := validateState(state); err != nil {
		return nil, err
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	limits := normalizeLimits(config.Limits)
	if err := validateLimits(limits); err != nil {
		return nil, err
	}
	return &IncidentWorkflow{incidentID: incidentID, intentIDPrefix: intentIDPrefix, state: state, now: now, limits: limits}, nil
}

// Apply 校验 Actor 和当前状态，并原子提交一次状态变化。
func (w *IncidentWorkflow) Apply(event Event) (Transition, error) {
	if w == nil {
		return Transition{}, fmt.Errorf("%w: workflow is not initialized", ErrInvalidTransition)
	}
	if !event.Actor.valid() {
		return Transition{}, fmt.Errorf("%w: unknown actor %q", ErrActorNotAllowed, event.Actor)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	return w.applyLocked(event)
}

func (w *IncidentWorkflow) applyLocked(event Event) (Transition, error) {
	from := w.state
	rule, exists := transitionRules[from][event.Type]
	if event.Type == EventEscalated && from != StateResolved && from != StateEscalated {
		rule = transitionRule{to: StateEscalated, actors: actors(ActorAgent, ActorWorkflow, ActorController, ActorHuman)}
		exists = true
	}
	if !exists {
		return Transition{}, fmt.Errorf("%w: event %q is not allowed from state %q", ErrInvalidTransition, event.Type, from)
	}
	if _, allowed := rule.actors[event.Actor]; !allowed {
		return Transition{}, fmt.Errorf("%w: actor %q cannot emit event %q from state %q", ErrActorNotAllowed, event.Actor, event.Type, from)
	}

	failure, err := w.eventFailureLocked(event)
	if err != nil {
		return Transition{}, err
	}

	to := rule.to
	if to == StateEscalated {
		w.suspendedState = from
	} else if from == StateEscalated {
		w.suspendedState = ""
	}

	w.version++
	if failure != nil {
		failure.WorkflowVersion = w.version
	}
	transition := Transition{
		Version: w.version, From: from, To: to, Event: event.Type, Actor: event.Actor,
		Reason: strings.TrimSpace(event.Reason), Metadata: cloneMetadata(event.Metadata),
		Failure: cloneStageFailurePointer(failure), At: w.now(),
	}
	w.state = to
	if event.Type == EventCheckpointNeedsAgent {
		w.agentResumesUsed++
	}
	if failure != nil {
		w.failures = append(w.failures, *failure)
	}
	w.history = append(w.history, transition)
	return cloneTransition(transition), nil
}

// Snapshot 返回不会与内部状态共享 slice 或 map 的只读副本。
func (w *IncidentWorkflow) Snapshot() Snapshot {
	if w == nil {
		return Snapshot{}
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	history := make([]Transition, len(w.history))
	for index := range w.history {
		history[index] = cloneTransition(w.history[index])
	}
	return Snapshot{
		IncidentID: w.incidentID, State: w.state, SuspendedState: w.suspendedState,
		Version: w.version, ExecutionIntent: cloneExecutionIntentPointer(w.intent), ExecutionIntents: cloneExecutionIntents(w.executionIntents), ActionDryRuns: cloneActionDryRuns(w.actionDryRuns),
		ActionPolicies: cloneActionPolicyDecisions(w.actionPolicies), ActionApprovals: cloneActionApprovals(w.actionApprovals),
		AllActionDryRuns: cloneActionDryRuns(w.allActionDryRuns), AllActionPolicies: cloneActionPolicyDecisions(w.allActionPolicies),
		AllActionApprovals: cloneActionApprovals(w.allActionApprovals), ActiveStageIndex: w.activeStageIndex,
		ResolvedActions: cloneResolvedActions(w.resolvedActions), ActionResults: cloneActionResults(w.actionResults),
		Checkpoints: cloneDecisionCheckpoints(w.checkpoints), Limits: w.limits,
		StagesExecuted: w.stagesExecuted, AgentResumesUsed: w.agentResumesUsed, ActionsAccepted: w.actionsAccepted,
		Failures: cloneStageFailures(w.failures), History: history,
	}
}

// RecordFailure 保存不引起状态转换的结构化失败，例如可重试的平台暂时不可用，
// 或需要先对账的未知执行结果。它不会推进 Workflow 版本或改变当前状态。
func (w *IncidentWorkflow) RecordFailure(value StageFailure) (StageFailure, error) {
	if w == nil {
		return StageFailure{}, fmt.Errorf("%w: workflow is not initialized", ErrInvalidTransition)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	value.WorkflowVersion = w.version
	normalized, err := normalizeStageFailure(value, w.now())
	if err != nil {
		return StageFailure{}, err
	}
	if expected, ok := failureStageForState(w.state); !ok || normalized.Stage != expected {
		return StageFailure{}, fmt.Errorf("failure stage %q does not match workflow state %q", normalized.Stage, w.state)
	}
	w.failures = append(w.failures, normalized)
	return normalized, nil
}

func failureStageForState(state State) (FailureStage, bool) {
	switch state {
	case StateValidating:
		return FailureStageDryRun, true
	case StateExecuting:
		return FailureStageActionExecution, true
	case StateCheckpoint:
		return FailureStageCheckpoint, true
	default:
		return "", false
	}
}

func (w *IncidentWorkflow) eventFailureLocked(event Event) (*StageFailure, error) {
	value := cloneStageFailurePointer(event.Failure)
	if value == nil {
		return nil, nil
	}
	normalized, err := normalizeStageFailure(*value, w.now())
	if err != nil {
		return nil, fmt.Errorf("normalize event failure: %w", err)
	}
	return &normalized, nil
}

func cloneTransition(value Transition) Transition {
	value.Metadata = cloneMetadata(value.Metadata)
	value.Failure = cloneStageFailurePointer(value.Failure)
	return value
}

func cloneMetadata(value map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}
