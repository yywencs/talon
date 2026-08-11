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
	At       time.Time         `json:"at"`
}

// Snapshot 是可用于审计和后续 checkpoint 的 Workflow 只读副本。
type Snapshot struct {
	IncidentID     string       `json:"incident_id"`
	State          State        `json:"state"`
	SuspendedState State        `json:"suspended_state,omitempty"`
	Version        uint64       `json:"version"`
	Plan           *Plan        `json:"plan,omitempty"`
	PlanDryRun     *PlanDryRun  `json:"plan_dry_run,omitempty"`
	History        []Transition `json:"history"`
}

// IncidentWorkflow 保存单个 Incident 的确定性生命周期状态。
// 它不执行 LLM 或平台操作，只负责判断“是否允许”和“下一状态是什么”。
type IncidentWorkflow struct {
	mu sync.RWMutex

	incidentID     string
	state          State
	suspendedState State
	version        uint64
	plan           *Plan
	planDryRun     *PlanDryRun
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
	},
	StatePlanned: {
		EventPlanApproved:     {to: StateRemediating, actors: actors(ActorWorkflow)},
		EventApprovalRequired: {to: StateAwaitingApproval, actors: actors(ActorWorkflow)},
		EventPlanRejected:     {to: StateInvestigating, actors: actors(ActorWorkflow, ActorHuman)},
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
	return &IncidentWorkflow{incidentID: incidentID, state: state, now: now}, nil
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
	transition := Transition{
		Version: w.version, From: from, To: to, Event: event.Type, Actor: event.Actor,
		Reason: strings.TrimSpace(event.Reason), Metadata: cloneMetadata(event.Metadata), At: w.now(),
	}
	w.state = to
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
		Version: w.version, Plan: clonePlanPointer(w.plan), PlanDryRun: clonePlanDryRunPointer(w.planDryRun), History: history,
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
