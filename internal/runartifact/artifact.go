// Package runartifact records the machine-readable audit trail of one Talon run.
package runartifact

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/workflow"
)

const SchemaVersion = "talon.run-artifact/v2"

// Provenance identifies the code and dataset that produced a run.
type Provenance struct {
	CodeVersion    string `json:"code_version"`
	DatasetVersion string `json:"dataset_version"`
}

// RunConfig records the inputs that can materially change Agent behavior.
// Secrets and endpoints must never be stored here.
type RunConfig struct {
	ModelProvider string `json:"model_provider,omitempty"`
	Model         string `json:"model,omitempty"`
	AgentMaxSteps int    `json:"agent_max_steps"`
	AutoApprove   bool   `json:"auto_approve"`
}

// ProviderState is the evaluation-safe subset of a Provider. Endpoint details
// are deliberately excluded from the durable artifact.
type ProviderState struct {
	ID               string                  `json:"id"`
	Health           platform.ProviderHealth `json:"health"`
	SchemaCompatible *bool                   `json:"schema_compatible,omitempty"`
}

// ConfigState excludes Simulator-only attributes that may contain hidden
// scenario mechanics while retaining the state needed by an evaluator.
type ConfigState struct {
	ID           string `json:"id"`
	Active       bool   `json:"active"`
	KnownHealthy bool   `json:"known_healthy"`
}

// ConnectionState excludes extensible attributes while retaining observable
// connection recovery results.
type ConnectionState struct {
	ProviderID              string     `json:"provider_id"`
	PoolGeneration          int        `json:"pool_generation"`
	ResolverCacheGeneration int        `json:"resolver_cache_generation"`
	ResolvedIP              string     `json:"resolved_ip,omitempty"`
	ActiveConnections       int        `json:"active_connections"`
	TargetConnections       int        `json:"target_connections"`
	ConfigFingerprint       string     `json:"config_fingerprint,omitempty"`
	LastPingAt              *time.Time `json:"last_ping_at,omitempty"`
}

// TaskState is the evaluation-safe subset of a managed asynchronous task.
type TaskState struct {
	ID         string              `json:"id"`
	Type       string              `json:"type"`
	Name       string              `json:"name"`
	Status     platform.TaskStatus `json:"status"`
	ProviderID string              `json:"provider_id,omitempty"`
	Attempts   int                 `json:"attempts"`
	Idempotent bool                `json:"idempotent"`
	LastError  string              `json:"last_error,omitempty"`
}

// FinalState is a deliberately reduced view of the managed world at the end
// of a run. It contains observable outcomes, not timelines or hidden causes.
type FinalState struct {
	WorkflowState workflow.State          `json:"workflow_state,omitempty"`
	VirtualTime   time.Time               `json:"virtual_time,omitempty"`
	Routes        []platform.Route        `json:"routes"`
	Providers     []ProviderState         `json:"providers"`
	Configs       []ConfigState           `json:"configs"`
	Connections   []ConnectionState       `json:"connections"`
	Tasks         []TaskState             `json:"tasks"`
	Traffic       platform.TrafficProfile `json:"traffic"`
}

type TokenUsage struct {
	PromptTokens       int `json:"prompt_tokens"`
	CompletionTokens   int `json:"completion_tokens"`
	TotalTokens        int `json:"total_tokens"`
	ReasoningTokens    int `json:"reasoning_tokens,omitempty"`
	CachedPromptTokens int `json:"cached_prompt_tokens,omitempty"`
}

type ModelCall struct {
	Sequence     int                 `json:"sequence"`
	StartedAt    time.Time           `json:"started_at"`
	FinishedAt   time.Time           `json:"finished_at"`
	Duration     time.Duration       `json:"duration"`
	FinishReason string              `json:"finish_reason,omitempty"`
	ToolCalls    []RequestedToolCall `json:"tool_calls,omitempty"`
	Usage        TokenUsage          `json:"usage"`
	Error        string              `json:"error,omitempty"`
}

type RequestedToolCall struct {
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type ToolCall struct {
	Sequence      int                  `json:"sequence"`
	CallID        string               `json:"call_id,omitempty"`
	Name          string               `json:"name"`
	Action        workflow.AgentAction `json:"action,omitempty"`
	Arguments     json.RawMessage      `json:"arguments,omitempty"`
	Output        json.RawMessage      `json:"output,omitempty"`
	Status        string               `json:"status"`
	Error         string               `json:"error,omitempty"`
	StartedAt     time.Time            `json:"started_at"`
	FinishedAt    time.Time            `json:"finished_at"`
	Duration      time.Duration        `json:"duration"`
	EvidenceRef   string               `json:"evidence_ref,omitempty"`
	IsNewEvidence bool                 `json:"is_new_evidence,omitempty"`
}

type BlockedAttempt struct {
	AgentRun int       `json:"agent_run"`
	ToolCall int       `json:"tool_call"`
	Name     string    `json:"name"`
	Reason   string    `json:"reason"`
	At       time.Time `json:"at"`
}

type AgentRun struct {
	Sequence        int             `json:"sequence"`
	Instruction     string          `json:"instruction"`
	InitialState    workflow.State  `json:"initial_state"`
	FinalState      workflow.State  `json:"final_state"`
	StartedAt       time.Time       `json:"started_at"`
	FinishedAt      time.Time       `json:"finished_at"`
	Duration        time.Duration   `json:"duration"`
	ModelCalls      []ModelCall     `json:"model_calls"`
	ToolCalls       []ToolCall      `json:"tool_calls"`
	Plans           []workflow.Plan `json:"plans"`
	NewEvidenceRefs []string        `json:"new_evidence_refs,omitempty"`
	Error           string          `json:"error,omitempty"`
}

type Summary struct {
	AgentRuns        int           `json:"agent_runs"`
	ModelCalls       int           `json:"model_calls"`
	ToolCalls        int           `json:"tool_calls"`
	InvalidToolCalls int           `json:"invalid_tool_calls"`
	BlockedAttempts  int           `json:"blocked_attempts"`
	PromptTokens     int           `json:"prompt_tokens"`
	CompletionTokens int           `json:"completion_tokens"`
	TotalTokens      int           `json:"total_tokens"`
	LLMDuration      time.Duration `json:"llm_duration"`
}

type Failure struct {
	Stage   string `json:"stage"`
	Message string `json:"message"`
}

type RunArtifact struct {
	SchemaVersion   string                `json:"schema_version"`
	Provenance      Provenance            `json:"provenance"`
	RunConfig       RunConfig             `json:"run_config"`
	RunID           string                `json:"run_id"`
	ScenarioID      string                `json:"scenario_id"`
	StartedAt       time.Time             `json:"started_at"`
	FinishedAt      time.Time             `json:"finished_at"`
	Duration        time.Duration         `json:"duration"`
	Outcome         string                `json:"outcome"`
	StopReason      string                `json:"stop_reason,omitempty"`
	Failure         *Failure              `json:"failure,omitempty"`
	AgentRuns       []AgentRun            `json:"agent_runs"`
	Plans           []workflow.Plan       `json:"plans"`
	WorkflowHistory []workflow.Transition `json:"workflow_history"`
	BlockedAttempts []BlockedAttempt      `json:"blocked_attempts"`
	Operations      []platform.Operation  `json:"operations"`
	FinalState      FinalState            `json:"final_state"`
	Summary         Summary               `json:"summary"`
}

type Recorder struct {
	mu               sync.Mutex
	artifact         RunArtifact
	currentRun       int
	currentPlanCount int
	evidence         map[string]struct{}
	now              func() time.Time
}

func New(scenarioID string, provenance Provenance, config RunConfig) *Recorder {
	now := time.Now
	return &Recorder{artifact: RunArtifact{SchemaVersion: SchemaVersion, Provenance: provenance, RunConfig: config, RunID: newRunID(), ScenarioID: strings.TrimSpace(scenarioID), StartedAt: now(), Outcome: "running", AgentRuns: []AgentRun{}, Plans: []workflow.Plan{}, WorkflowHistory: []workflow.Transition{}, BlockedAttempts: []BlockedAttempt{}, Operations: []platform.Operation{}, FinalState: FinalState{Routes: []platform.Route{}, Providers: []ProviderState{}, Configs: []ConfigState{}, Connections: []ConnectionState{}, Tasks: []TaskState{}}}, evidence: map[string]struct{}{}, now: now}
}

// RecordFinalState records the platform-independent operations and reduced
// final world state used by offline evaluation.
func (r *Recorder) RecordFinalState(operations []platform.Operation, state FinalState) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.artifact.Operations = cloneOperations(operations)
	r.artifact.FinalState = cloneFinalState(state)
}

func (r *Recorder) BeginAgentRun(instruction string, snapshot workflow.Snapshot) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.now()
	r.artifact.AgentRuns = append(r.artifact.AgentRuns, AgentRun{Sequence: len(r.artifact.AgentRuns) + 1, Instruction: strings.TrimSpace(instruction), InitialState: snapshot.State, StartedAt: n, ModelCalls: []ModelCall{}, ToolCalls: []ToolCall{}, Plans: []workflow.Plan{}})
	r.currentRun = len(r.artifact.AgentRuns)
	r.currentPlanCount = len(snapshot.Plans)
}

func (r *Recorder) EndAgentRun(snapshot workflow.Snapshot, err error) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.currentRun == 0 {
		return
	}
	run := &r.artifact.AgentRuns[r.currentRun-1]
	run.FinalState, run.FinishedAt = snapshot.State, r.now()
	run.Duration = run.FinishedAt.Sub(run.StartedAt)
	if r.currentPlanCount < len(snapshot.Plans) {
		run.Plans = clonePlans(snapshot.Plans[r.currentPlanCount:])
	}
	r.artifact.Plans = clonePlans(snapshot.Plans)
	r.artifact.WorkflowHistory = append([]workflow.Transition{}, snapshot.History...)
	if err != nil {
		run.Error = err.Error()
	}
	r.currentRun = 0
	r.currentPlanCount = 0
}

func (r *Recorder) RecordModelCall(start time.Time, message *schema.Message, err error) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.currentRun == 0 {
		return
	}
	run := &r.artifact.AgentRuns[r.currentRun-1]
	end := r.now()
	call := ModelCall{Sequence: len(run.ModelCalls) + 1, StartedAt: start, FinishedAt: end, Duration: end.Sub(start)}
	if err != nil {
		call.Error = err.Error()
	}
	if message != nil {
		if message.ResponseMeta != nil {
			call.FinishReason = message.ResponseMeta.FinishReason
			if u := message.ResponseMeta.Usage; u != nil {
				call.Usage = TokenUsage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, TotalTokens: u.TotalTokens, ReasoningTokens: u.CompletionTokensDetails.ReasoningTokens, CachedPromptTokens: u.PromptTokenDetails.CachedTokens}
			}
		}
		for _, tc := range message.ToolCalls {
			call.ToolCalls = append(call.ToolCalls, RequestedToolCall{CallID: tc.ID, Name: tc.Function.Name, Arguments: rawJSON(tc.Function.Arguments)})
		}
	}
	run.ModelCalls = append(run.ModelCalls, call)
}

func (r *Recorder) RecordToolCall(callID, name string, action workflow.AgentAction, arguments, output string, started time.Time, err error, denied bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.currentRun == 0 {
		return
	}
	run := &r.artifact.AgentRuns[r.currentRun-1]
	end := r.now()
	call := ToolCall{Sequence: len(run.ToolCalls) + 1, CallID: callID, Name: name, Action: action, Arguments: rawJSON(arguments), Output: rawJSON(output), Status: "succeeded", StartedAt: started, FinishedAt: end, Duration: end.Sub(started)}
	if denied {
		call.Status = "denied"
	} else if err != nil || responseError(output) != "" {
		call.Status = "failed"
	}
	if err != nil {
		call.Error = err.Error()
	} else {
		call.Error = responseError(output)
	}
	if action == workflow.AgentActionRead && call.Status == "succeeded" {
		h := sha256.Sum256(append([]byte(name+"\x00"), call.Output...))
		call.EvidenceRef = "tool:" + name + ":" + hex.EncodeToString(h[:8])
		if _, exists := r.evidence[call.EvidenceRef]; !exists {
			call.IsNewEvidence = true
			r.evidence[call.EvidenceRef] = struct{}{}
			run.NewEvidenceRefs = append(run.NewEvidenceRefs, call.EvidenceRef)
		}
	}
	run.ToolCalls = append(run.ToolCalls, call)
	if denied {
		r.artifact.BlockedAttempts = append(r.artifact.BlockedAttempts, BlockedAttempt{AgentRun: run.Sequence, ToolCall: call.Sequence, Name: name, Reason: call.Error, At: end})
	}
}

func (r *Recorder) RecordUnknownTool(name, arguments, output string, err error) {
	started := r.now()
	r.RecordToolCall("", name, "", arguments, output, started, err, true)
}

// ValidateEvidenceRefs 确认引用对应本次运行中已经成功完成的只读工具调用。
// Agent 可以引用工具调用 ID，或 Artifact 生成的稳定 evidence_ref。
func (r *Recorder) ValidateEvidenceRefs(refs []string) error {
	if r == nil {
		return fmt.Errorf("run artifact recorder is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	available := make(map[string]struct{})
	for _, run := range r.artifact.AgentRuns {
		for _, call := range run.ToolCalls {
			if call.Status != "succeeded" || call.Action != workflow.AgentActionRead {
				continue
			}
			if call.CallID != "" {
				available[call.CallID] = struct{}{}
			}
			if call.EvidenceRef != "" {
				available[call.EvidenceRef] = struct{}{}
			}
		}
	}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if _, exists := available[ref]; !exists {
			return fmt.Errorf("evidence reference %q does not identify a successful read tool call", ref)
		}
	}
	return nil
}

func (r *Recorder) Finish(stopReason string, snapshot workflow.Snapshot, err error) RunArtifact {
	if r == nil {
		return RunArtifact{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.artifact.FinishedAt = r.now()
	r.artifact.Duration = r.artifact.FinishedAt.Sub(r.artifact.StartedAt)
	r.artifact.StopReason = stopReason
	r.artifact.Outcome = "completed"
	if err != nil {
		r.artifact.Outcome = "failed"
		r.artifact.Failure = &Failure{Stage: stageFor(snapshot.State), Message: err.Error()}
	}
	r.artifact.Plans = clonePlans(snapshot.Plans)
	r.artifact.WorkflowHistory = append([]workflow.Transition{}, snapshot.History...)
	r.rebuildSummaryLocked()
	return cloneArtifact(r.artifact)
}

func (r *Recorder) Snapshot() RunArtifact {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rebuildSummaryLocked()
	return cloneArtifact(r.artifact)
}

func (r *Recorder) rebuildSummaryLocked() {
	s := Summary{AgentRuns: len(r.artifact.AgentRuns), BlockedAttempts: len(r.artifact.BlockedAttempts)}
	for _, run := range r.artifact.AgentRuns {
		s.ModelCalls += len(run.ModelCalls)
		s.ToolCalls += len(run.ToolCalls)
		for _, c := range run.ModelCalls {
			s.PromptTokens += c.Usage.PromptTokens
			s.CompletionTokens += c.Usage.CompletionTokens
			s.TotalTokens += c.Usage.TotalTokens
			s.LLMDuration += c.Duration
		}
		for _, c := range run.ToolCalls {
			if c.Status != "succeeded" {
				s.InvalidToolCalls++
			}
		}
	}
	r.artifact.Summary = s
}

func stageFor(state workflow.State) string {
	if state == "" {
		return "initialization"
	}
	return string(state)
}
func rawJSON(value string) json.RawMessage {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if json.Valid([]byte(value)) {
		return json.RawMessage(value)
	}
	encoded, _ := json.Marshal(value)
	return encoded
}
func responseError(value string) string {
	var v struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(value), &v) == nil {
		return strings.TrimSpace(v.Error)
	}
	return ""
}
func cloneArtifact(value RunArtifact) RunArtifact {
	raw, _ := json.Marshal(value)
	var result RunArtifact
	_ = json.Unmarshal(raw, &result)
	return Normalize(result)
}

// Normalize keeps all collection fields as JSON arrays, including artifacts
// written by older code paths that persisted nil slices as null.
func Normalize(value RunArtifact) RunArtifact {
	if value.AgentRuns == nil {
		value.AgentRuns = []AgentRun{}
	}
	if value.Plans == nil {
		value.Plans = []workflow.Plan{}
	}
	if value.WorkflowHistory == nil {
		value.WorkflowHistory = []workflow.Transition{}
	}
	if value.BlockedAttempts == nil {
		value.BlockedAttempts = []BlockedAttempt{}
	}
	if value.Operations == nil {
		value.Operations = []platform.Operation{}
	}
	for index := range value.AgentRuns {
		if value.AgentRuns[index].ModelCalls == nil {
			value.AgentRuns[index].ModelCalls = []ModelCall{}
		}
		if value.AgentRuns[index].ToolCalls == nil {
			value.AgentRuns[index].ToolCalls = []ToolCall{}
		}
		if value.AgentRuns[index].Plans == nil {
			value.AgentRuns[index].Plans = []workflow.Plan{}
		}
	}
	if value.FinalState.Routes == nil {
		value.FinalState.Routes = []platform.Route{}
	}
	if value.FinalState.Providers == nil {
		value.FinalState.Providers = []ProviderState{}
	}
	if value.FinalState.Configs == nil {
		value.FinalState.Configs = []ConfigState{}
	}
	if value.FinalState.Connections == nil {
		value.FinalState.Connections = []ConnectionState{}
	}
	if value.FinalState.Tasks == nil {
		value.FinalState.Tasks = []TaskState{}
	}
	return value
}

func clonePlans(values []workflow.Plan) []workflow.Plan {
	if values == nil {
		return []workflow.Plan{}
	}
	raw, _ := json.Marshal(values)
	var result []workflow.Plan
	_ = json.Unmarshal(raw, &result)
	return result
}

func cloneOperations(values []platform.Operation) []platform.Operation {
	if values == nil {
		return []platform.Operation{}
	}
	raw, _ := json.Marshal(values)
	var result []platform.Operation
	_ = json.Unmarshal(raw, &result)
	return result
}

func cloneFinalState(value FinalState) FinalState {
	raw, _ := json.Marshal(value)
	var result FinalState
	_ = json.Unmarshal(raw, &result)
	return Normalize(RunArtifact{FinalState: result}).FinalState
}

func newRunID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic("generate run artifact ID: " + err.Error())
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}
