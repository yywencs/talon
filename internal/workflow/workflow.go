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
	IncidentID   string
	PlanIDPrefix string
	InitialState State
	Now          func() time.Time
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
	IncidentID     string               `json:"incident_id"`
	State          State                `json:"state"`
	SuspendedState State                `json:"suspended_state,omitempty"`
	Version        uint64               `json:"version"`
	Plan           *Plan                `json:"plan,omitempty"`
	Plans          []Plan               `json:"plans"`
	PlanDryRuns    []PlanDryRun         `json:"plan_dry_runs,omitempty"`
	PlanPolicies   []PlanPolicyDecision `json:"plan_policies,omitempty"`
	PlanApprovals  []PlanApproval       `json:"plan_approvals,omitempty"`
	Failures       []StageFailure       `json:"failures,omitempty"`
	History        []Transition         `json:"history"`
}

// IncidentWorkflow 保存单个 Incident 的确定性生命周期状态。
// 它不执行 LLM 或平台操作，只负责判断“是否允许”和“下一状态是什么”。
type IncidentWorkflow struct {
	mu sync.RWMutex

	incidentID     string
	planIDPrefix   string
	state          State
	suspendedState State
	version        uint64
	plan           *Plan
	plans          []Plan
	planDryRuns    []PlanDryRun
	planPolicies   []PlanPolicyDecision
	planApprovals  []PlanApproval
	failures       []StageFailure
	history        []Transition
	now            func() time.Time
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
		EventPlanSubmitted: {to: StatePlanned, actors: actors(ActorAgent)},
		EventSkillLoaded:   {to: StateInvestigating, actors: actors(ActorAgent)},
		EventSkillUnloaded: {to: StateInvestigating, actors: actors(ActorAgent)},
	},
	StatePlanned: {
		EventPlanApproved:     {to: StateRemediating, actors: actors(ActorWorkflow)},
		EventApprovalRequired: {to: StateAwaitingApproval, actors: actors(ActorWorkflow)},
		EventPlanRejected:     {to: StateReinvestigating, actors: actors(ActorWorkflow, ActorHuman)},
	},
	StateAwaitingApproval: {
		EventPlanApproved: {to: StateRemediating, actors: actors(ActorHuman)},
		EventPlanRejected: {to: StateReinvestigating, actors: actors(ActorHuman)},
	},
	StateRemediating: {
		EventStageSucceeded:       {to: StateProbing, actors: actors(ActorWorkflow)},
		EventStageFailed:          {to: StateReinvestigating, actors: actors(ActorWorkflow)},
		EventCompensationRequired: {to: StateCompensating, actors: actors(ActorWorkflow, ActorController)},
	},
	StateProbing: {
		EventStageSucceeded:       {to: StateRecovering, actors: actors(ActorWorkflow, ActorController)},
		EventStageFailed:          {to: StateReinvestigating, actors: actors(ActorWorkflow, ActorController)},
		EventCompensationRequired: {to: StateCompensating, actors: actors(ActorWorkflow, ActorController)},
	},
	StateRecovering: {
		EventStageSucceeded:       {to: StateResolved, actors: actors(ActorWorkflow, ActorController)},
		EventStageFailed:          {to: StateReinvestigating, actors: actors(ActorWorkflow, ActorController)},
		EventCompensationRequired: {to: StateCompensating, actors: actors(ActorWorkflow, ActorController)},
	},
	StateReinvestigating: {
		EventPlanSubmitted: {to: StatePlanned, actors: actors(ActorAgent)},
		EventSkillLoaded:   {to: StateReinvestigating, actors: actors(ActorAgent)},
		EventSkillUnloaded: {to: StateReinvestigating, actors: actors(ActorAgent)},
	},
	StateCompensating: {
		EventStageSucceeded: {to: StateReinvestigating, actors: actors(ActorWorkflow)},
		EventStageFailed:    {to: StateEscalated, actors: actors(ActorWorkflow)},
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
	planIDPrefix := strings.TrimSpace(config.PlanIDPrefix)
	if planIDPrefix == "" {
		planIDPrefix = incidentID
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
	return &IncidentWorkflow{incidentID: incidentID, planIDPrefix: planIDPrefix, state: state, now: now}, nil
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

	failure, err := w.eventFailureLocked(from, event)
	if err != nil {
		return Transition{}, err
	}

	to := rule.to
	if from == StateEscalated && event.Type == EventHumanResumed && resumesAsReinvestigation(w.suspendedState) {
		to = StateReinvestigating
	}
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
		Version: w.version, Plan: clonePlanPointer(w.plan), Plans: clonePlans(w.plans), PlanDryRuns: clonePlanDryRuns(w.planDryRuns),
		PlanPolicies: clonePlanPolicyDecisions(w.planPolicies), PlanApprovals: clonePlanApprovals(w.planApprovals),
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
	case StatePlanned:
		return FailureStageDryRun, true
	case StateRemediating:
		return FailureStageRemediation, true
	case StateProbing:
		return FailureStageProbe, true
	case StateRecovering:
		return FailureStageRecovery, true
	case StateCompensating:
		return FailureStageCompensation, true
	default:
		return "", false
	}
}

func (w *IncidentWorkflow) eventFailureLocked(from State, event Event) (*StageFailure, error) {
	value := cloneStageFailurePointer(event.Failure)
	if value == nil && event.Type == EventStageFailed {
		value = fallbackStageFailure(from, event.Reason, event.Metadata)
	}
	if value == nil {
		return nil, nil
	}
	normalized, err := normalizeStageFailure(*value, w.now())
	if err != nil {
		return nil, fmt.Errorf("normalize event failure: %w", err)
	}
	return &normalized, nil
}

func fallbackStageFailure(state State, message string, metadata map[string]string) *StageFailure {
	stage := FailureStage("")
	next := FailureNextReinvestigate
	switch state {
	case StateRemediating:
		stage = FailureStageRemediation
	case StateProbing:
		stage = FailureStageProbe
	case StateRecovering:
		stage = FailureStageRecovery
	case StateCompensating:
		stage = FailureStageCompensation
		next = FailureNextEscalate
	default:
		return nil
	}
	return &StageFailure{
		Stage: stage, Category: FailureCategoryUnclassified, Code: "unclassified_error",
		SafeSummary: "当前执行阶段发生了未分类错误", Message: strings.TrimSpace(message),
		NextAction: next, Fallback: true, PlanID: metadata["plan_id"],
		ActionID: metadata["action_id"], OperationID: metadata["operation_id"],
		OperationStatus: metadata["operation_status"],
	}
}

func resumesAsReinvestigation(state State) bool {
	switch state {
	case StateRemediating, StateProbing, StateRecovering, StateReinvestigating, StateCompensating:
		return true
	default:
		return false
	}
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
