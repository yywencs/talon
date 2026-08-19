// Package runartifact records the machine-readable audit trail of one Talon run.
package runartifact

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/workflow"
)

const SchemaVersion = "talon.run-artifact/v3"

const (
	CapabilityCanonicalEvidenceIDs        = "canonical_evidence_ids"
	CapabilityEvidenceLookup              = "evidence_lookup"
	CapabilityIncidentContextSnapshot     = "incident_context_snapshot"
	CapabilityPerModelContextSnapshot     = "per_model_context_snapshot"
	CapabilityStructuredExperience        = "structured_experience"
	CapabilityStructuredEscalationHandoff = "structured_escalation_handoff"
	CapabilityStructuredStageFailures     = "structured_stage_failures"
	CapabilityDynamicExecutionStages      = "dynamic_execution_stages"
	CapabilityTypedActionOutputReferences = "typed_action_output_references"
	CapabilityDecisionCheckpoints         = "decision_checkpoints"
	CapabilityExecutionIntents            = "execution_intents"
)

var currentCapabilities = []string{
	CapabilityCanonicalEvidenceIDs,
	CapabilityEvidenceLookup,
	CapabilityIncidentContextSnapshot,
	CapabilityPerModelContextSnapshot,
	CapabilityStructuredEscalationHandoff,
	CapabilityStructuredExperience,
	CapabilityStructuredStageFailures,
	CapabilityDynamicExecutionStages,
	CapabilityTypedActionOutputReferences,
	CapabilityDecisionCheckpoints,
	CapabilityExecutionIntents,
}

// Provenance identifies the code and dataset that produced a run.
type Provenance struct {
	CodeVersion    string `json:"code_version"`
	DatasetVersion string `json:"dataset_version"`
	PromptVersion  string `json:"prompt_version,omitempty"`
	PromptDigest   string `json:"prompt_digest,omitempty"`
}

// RunConfig records the inputs that can materially change Agent behavior.
// Secrets and endpoints must never be stored here.
type RunConfig struct {
	ModelProvider  string `json:"model_provider,omitempty"`
	Model          string `json:"model,omitempty"`
	AgentMaxSteps  int    `json:"agent_max_steps"`
	MaxModelCalls  int    `json:"max_model_calls"`
	AutoApprove    bool   `json:"auto_approve"`
	ContextVersion string `json:"context_version,omitempty"`
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
	Sequence        int                      `json:"sequence"`
	StartedAt       time.Time                `json:"started_at"`
	FinishedAt      time.Time                `json:"finished_at"`
	Duration        time.Duration            `json:"duration"`
	FinishReason    string                   `json:"finish_reason,omitempty"`
	ToolCalls       []RequestedToolCall      `json:"tool_calls,omitempty"`
	Usage           TokenUsage               `json:"usage"`
	Error           string                   `json:"error,omitempty"`
	ContextSnapshot *IncidentContextSnapshot `json:"context_snapshot,omitempty"`
	ModelProvider   string                   `json:"model_provider,omitempty"`
	Model           string                   `json:"model,omitempty"`
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
	EvidenceIDs   []string             `json:"evidence_ids"`
	IsNewEvidence bool                 `json:"is_new_evidence,omitempty"`
}

// EvidenceRecord 是按 Evidence Ref 从当前 RunArtifact 取回的不可变历史观察。
// Output 仍属于不可信外部数据，调用方必须在提供给模型前进行脱敏和大小限制。
type EvidenceRecord struct {
	EvidenceRef string          `json:"evidence_ref"`
	EvidenceIDs []string        `json:"evidence_ids"`
	SourceTool  string          `json:"source_tool"`
	AgentRun    int             `json:"agent_run"`
	ObservedAt  time.Time       `json:"observed_at"`
	Output      json.RawMessage `json:"output"`
}

type BlockedAttempt struct {
	AgentRun int       `json:"agent_run"`
	ToolCall int       `json:"tool_call"`
	Name     string    `json:"name"`
	Reason   string    `json:"reason"`
	At       time.Time `json:"at"`
}

type AgentRun struct {
	Sequence         int                        `json:"sequence"`
	Instruction      string                     `json:"instruction"`
	InitialState     workflow.State             `json:"initial_state"`
	FinalState       workflow.State             `json:"final_state"`
	StartedAt        time.Time                  `json:"started_at"`
	FinishedAt       time.Time                  `json:"finished_at"`
	Duration         time.Duration              `json:"duration"`
	ModelCalls       []ModelCall                `json:"model_calls"`
	ToolCalls        []ToolCall                 `json:"tool_calls"`
	ExecutionIntents []workflow.ExecutionIntent `json:"execution_intents"`
	NewEvidenceRefs  []string                   `json:"new_evidence_refs,omitempty"`
	Error            string                     `json:"error,omitempty"`
	ContextSnapshot  *IncidentContextSnapshot   `json:"context_snapshot,omitempty"`
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
	Stage           string    `json:"stage"`
	Category        string    `json:"category,omitempty"`
	Code            string    `json:"code,omitempty"`
	SafeSummary     string    `json:"safe_summary,omitempty"`
	Message         string    `json:"message"`
	NextAction      string    `json:"next_action,omitempty"`
	Retryable       bool      `json:"retryable,omitempty"`
	Fallback        bool      `json:"fallback,omitempty"`
	IntentID        string    `json:"intent_id,omitempty"`
	ActionID        string    `json:"action_id,omitempty"`
	OperationID     string    `json:"operation_id,omitempty"`
	OperationStatus string    `json:"operation_status,omitempty"`
	OccurredAt      time.Time `json:"occurred_at,omitempty"`
}

// IncidentExperience is a deterministic index over facts already retained in
// the Artifact. Sources point back to ExecutionIntent, ToolCall, Operation, or provenance
// identifiers so completeness can be evaluated without interpreting prose.
type IncidentExperience struct {
	Fields  []string            `json:"fields"`
	Sources map[string][]string `json:"sources"`
}

type RunArtifact struct {
	SchemaVersion    string                          `json:"schema_version"`
	Capabilities     []string                        `json:"capabilities"`
	Provenance       Provenance                      `json:"provenance"`
	RunConfig        RunConfig                       `json:"run_config"`
	RunID            string                          `json:"run_id"`
	ScenarioID       string                          `json:"scenario_id"`
	StartedAt        time.Time                       `json:"started_at"`
	FinishedAt       time.Time                       `json:"finished_at"`
	Duration         time.Duration                   `json:"duration"`
	Outcome          string                          `json:"outcome"`
	StopReason       string                          `json:"stop_reason,omitempty"`
	Failure          *Failure                        `json:"failure,omitempty"`
	AgentRuns        []AgentRun                      `json:"agent_runs"`
	ExecutionIntents []workflow.ExecutionIntent      `json:"execution_intents"`
	WorkflowHistory  []workflow.Transition           `json:"workflow_history"`
	StageFailures    []workflow.StageFailure         `json:"stage_failures"`
	ResolvedActions  []workflow.ResolvedAction       `json:"resolved_actions"`
	ActionResults    []workflow.ActionResult         `json:"action_results"`
	ActionDryRuns    []workflow.ActionDryRun         `json:"action_dry_runs"`
	ActionPolicies   []workflow.ActionPolicyDecision `json:"action_policies"`
	ActionApprovals  []workflow.ActionApproval       `json:"action_approvals"`
	Checkpoints      []workflow.DecisionCheckpoint   `json:"decision_checkpoints"`
	BlockedAttempts  []BlockedAttempt                `json:"blocked_attempts"`
	Operations       []platform.Operation            `json:"operations"`
	FinalState       FinalState                      `json:"final_state"`
	Experience       IncidentExperience              `json:"experience"`
	Summary          Summary                         `json:"summary"`
}

type Recorder struct {
	mu                 sync.Mutex
	artifact           RunArtifact
	currentRun         int
	currentIntentCount int
	evidence           map[string]struct{}
	now                func() time.Time
}

func New(scenarioID string, provenance Provenance, config RunConfig) *Recorder {
	now := time.Now
	if strings.TrimSpace(config.ContextVersion) == "" {
		config.ContextVersion = IncidentContextSchemaVersion
	}
	return &Recorder{artifact: RunArtifact{SchemaVersion: SchemaVersion, Capabilities: append([]string(nil), currentCapabilities...), Provenance: provenance, RunConfig: config, RunID: newRunID(), ScenarioID: strings.TrimSpace(scenarioID), StartedAt: now(), Outcome: "running", AgentRuns: []AgentRun{}, ExecutionIntents: []workflow.ExecutionIntent{}, WorkflowHistory: []workflow.Transition{}, StageFailures: []workflow.StageFailure{}, ResolvedActions: []workflow.ResolvedAction{}, ActionResults: []workflow.ActionResult{}, ActionDryRuns: []workflow.ActionDryRun{}, ActionPolicies: []workflow.ActionPolicyDecision{}, ActionApprovals: []workflow.ActionApproval{}, Checkpoints: []workflow.DecisionCheckpoint{}, BlockedAttempts: []BlockedAttempt{}, Operations: []platform.Operation{}, FinalState: FinalState{Routes: []platform.Route{}, Providers: []ProviderState{}, Configs: []ConfigState{}, Connections: []ConnectionState{}, Tasks: []TaskState{}}, Experience: IncidentExperience{Fields: []string{}, Sources: map[string][]string{}}}, evidence: map[string]struct{}{}, now: now}
}

func (r *Recorder) RecordContextSnapshot(snapshot IncidentContextSnapshot) error {
	if r == nil {
		return fmt.Errorf("run artifact recorder is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.currentRun == 0 {
		return fmt.Errorf("an Agent run must be active before recording context")
	}
	sealed := SealIncidentContextSnapshot(snapshot)
	r.artifact.AgentRuns[r.currentRun-1].ContextSnapshot = &sealed
	return nil
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

// RecordWorkflowCheckpoint 把非 Agent 阶段的最新确定性状态同步进运行中的 Artifact。
// 调用方随后可用 Snapshot + Store.Upsert 形成可恢复的持久化检查点。
func (r *Recorder) RecordWorkflowCheckpoint(snapshot workflow.Snapshot) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.artifact.ExecutionIntents = cloneExecutionIntents(snapshot.ExecutionIntents)
	r.artifact.WorkflowHistory = append([]workflow.Transition{}, snapshot.History...)
	r.artifact.StageFailures = append([]workflow.StageFailure{}, snapshot.Failures...)
	r.syncDynamicWorkflowLocked(snapshot)
}

func (r *Recorder) BeginAgentRun(instruction string, snapshot workflow.Snapshot) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.now()
	r.artifact.AgentRuns = append(r.artifact.AgentRuns, AgentRun{Sequence: len(r.artifact.AgentRuns) + 1, Instruction: strings.TrimSpace(instruction), InitialState: snapshot.State, StartedAt: n, ModelCalls: []ModelCall{}, ToolCalls: []ToolCall{}, ExecutionIntents: []workflow.ExecutionIntent{}})
	r.currentRun = len(r.artifact.AgentRuns)
	r.currentIntentCount = len(snapshot.ExecutionIntents)
	r.syncDynamicWorkflowLocked(snapshot)
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
	if r.currentIntentCount < len(snapshot.ExecutionIntents) {
		run.ExecutionIntents = cloneExecutionIntents(snapshot.ExecutionIntents[r.currentIntentCount:])
	}
	r.artifact.ExecutionIntents = cloneExecutionIntents(snapshot.ExecutionIntents)
	r.artifact.WorkflowHistory = append([]workflow.Transition{}, snapshot.History...)
	r.artifact.StageFailures = append([]workflow.StageFailure{}, snapshot.Failures...)
	r.syncDynamicWorkflowLocked(snapshot)
	if err != nil {
		run.Error = err.Error()
	}
	r.currentRun = 0
	r.currentIntentCount = 0
}

func (r *Recorder) RecordModelCall(start time.Time, message *schema.Message, err error) {
	r.recordModelCall(start, message, err, nil)
}

// RecordModelCallWithContext 保存模型调用结果及调用前实际注入的上下文快照。
func (r *Recorder) RecordModelCallWithContext(start time.Time, message *schema.Message, err error, snapshot IncidentContextSnapshot) {
	r.recordModelCall(start, message, err, &snapshot)
}

func (r *Recorder) recordModelCall(start time.Time, message *schema.Message, err error, snapshot *IncidentContextSnapshot) {
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
	call := ModelCall{Sequence: len(run.ModelCalls) + 1, StartedAt: start, FinishedAt: end, Duration: end.Sub(start),
		ModelProvider: r.artifact.RunConfig.ModelProvider, Model: r.artifact.RunConfig.Model}
	if snapshot != nil {
		sealed := SealIncidentContextSnapshot(*snapshot)
		call.ContextSnapshot = &sealed
	}
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
		call.EvidenceIDs = responseEvidenceIDs(output)
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

// GetEvidence 按稳定 Evidence Ref 返回当前 Incident 中成功只读调用的历史观察。
// 它不接受 Call ID，避免调用方混淆模型请求标识与 Artifact 证据标识。
func (r *Recorder) GetEvidence(ref string) (EvidenceRecord, error) {
	if r == nil {
		return EvidenceRecord{}, fmt.Errorf("run artifact recorder is required")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return EvidenceRecord{}, fmt.Errorf("evidence_ref is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, run := range r.artifact.AgentRuns {
		for _, call := range run.ToolCalls {
			if call.EvidenceRef != ref || call.Action != workflow.AgentActionRead || call.Status != "succeeded" {
				continue
			}
			return EvidenceRecord{
				EvidenceRef: call.EvidenceRef, EvidenceIDs: append([]string(nil), call.EvidenceIDs...),
				SourceTool: call.Name, AgentRun: run.Sequence, ObservedAt: call.FinishedAt,
				Output: append(json.RawMessage(nil), call.Output...),
			}, nil
		}
	}
	return EvidenceRecord{}, fmt.Errorf("evidence %q was not found in the current Incident", ref)
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
	if err != nil || snapshot.State == workflow.StateFailed {
		r.artifact.Outcome = "failed"
		if err == nil {
			err = fmt.Errorf("workflow stopped in failed state")
		}
		r.artifact.Failure = artifactFailure(snapshot, err)
	}
	r.artifact.ExecutionIntents = cloneExecutionIntents(snapshot.ExecutionIntents)
	r.artifact.WorkflowHistory = append([]workflow.Transition{}, snapshot.History...)
	r.artifact.StageFailures = append([]workflow.StageFailure{}, snapshot.Failures...)
	r.syncDynamicWorkflowLocked(snapshot)
	r.rebuildExperienceLocked()
	r.rebuildSummaryLocked()
	return cloneArtifact(r.artifact)
}

func (r *Recorder) syncDynamicWorkflowLocked(snapshot workflow.Snapshot) {
	r.artifact.ResolvedActions = append([]workflow.ResolvedAction(nil), snapshot.ResolvedActions...)
	r.artifact.ActionResults = append([]workflow.ActionResult(nil), snapshot.ActionResults...)
	r.artifact.ActionDryRuns = append([]workflow.ActionDryRun(nil), snapshot.AllActionDryRuns...)
	r.artifact.ActionPolicies = append([]workflow.ActionPolicyDecision(nil), snapshot.AllActionPolicies...)
	r.artifact.ActionApprovals = append([]workflow.ActionApproval(nil), snapshot.AllActionApprovals...)
	r.artifact.Checkpoints = append([]workflow.DecisionCheckpoint(nil), snapshot.Checkpoints...)
}

func artifactFailure(snapshot workflow.Snapshot, err error) *Failure {
	result := &Failure{Stage: stageFor(snapshot.State), Message: err.Error()}
	if len(snapshot.Failures) == 0 {
		result.Category = string(workflow.FailureCategoryUnclassified)
		result.Code = "unclassified_run_error"
		result.SafeSummary = "运行在当前阶段因未分类错误而终止"
		result.NextAction = string(workflow.FailureNextEscalate)
		result.Fallback = true
		return result
	}
	latest := snapshot.Failures[len(snapshot.Failures)-1]
	result.Stage = string(latest.Stage)
	result.Category = string(latest.Category)
	result.Code = latest.Code
	result.SafeSummary = latest.SafeSummary
	result.NextAction = string(latest.NextAction)
	result.Retryable = latest.Retryable
	result.Fallback = latest.Fallback
	result.IntentID = latest.IntentID
	result.ActionID = latest.ActionID
	result.OperationID = latest.OperationID
	result.OperationStatus = latest.OperationStatus
	result.OccurredAt = latest.OccurredAt
	return result
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

func (r *Recorder) rebuildExperienceLocked() {
	sources := make(map[string]map[string]struct{})
	postFirstRunEvidence := make([]string, 0)
	add := func(field string, values ...string) {
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if sources[field] == nil {
				sources[field] = make(map[string]struct{})
			}
			sources[field][value] = struct{}{}
		}
	}
	for runIndex, run := range r.artifact.AgentRuns {
		for _, call := range run.ToolCalls {
			if call.Action != workflow.AgentActionRead || call.Status != "succeeded" {
				continue
			}
			ref := call.EvidenceRef
			if ref == "" {
				ref = call.CallID
			}
			add("symptoms", ref)
			if runIndex > 0 {
				postFirstRunEvidence = append(postFirstRunEvidence, ref)
			}
		}
	}
	for index, intent := range r.artifact.ExecutionIntents {
		add("evidence", intent.EvidenceRefs...)
		if strings.TrimSpace(intent.RootCause) != "" {
			add("root_cause", intent.ID)
			if index == 0 {
				add("initial_hypothesis", intent.ID)
			}
			if index == len(r.artifact.ExecutionIntents)-1 {
				add("final_root_cause", intent.ID)
			}
		}
	}
	remediations := make([]platform.Operation, 0)
	remediationsBeforeProbe := make([]platform.Operation, 0)
	failedProbe := false
	for _, operation := range r.artifact.Operations {
		switch operation.Kind {
		case platform.OperationRemediation:
			if operation.Status == platform.OperationSucceeded && operation.Result["dry_run"] != true {
				remediations = append(remediations, operation)
				remediationsBeforeProbe = append(remediationsBeforeProbe, operation)
				add("remediation", operation.ID)
			}
		case platform.OperationProbe:
			if operation.Status == platform.OperationSucceeded {
				add("probe_result", operation.ID)
			}
			if outcome, _ := operation.Result["outcome"].(string); outcome == "hard_stop" {
				failedProbe = true
				if len(remediationsBeforeProbe) > 0 {
					add("ineffective_remediation", remediationsBeforeProbe[len(remediationsBeforeProbe)-1].ID)
				}
			}
			remediationsBeforeProbe = remediationsBeforeProbe[:0]
		case platform.OperationEscalation:
			if operation.Status != platform.OperationSucceeded {
				continue
			}
			if reason, _ := operation.Result["reason"].(string); strings.TrimSpace(reason) != "" {
				add("root_cause", operation.ID)
			}
			if code, _ := operation.Result["reason_code"].(string); strings.TrimSpace(code) != "" {
				add("escalation_reason", operation.ID)
			}
			add("evidence", stringsFromAny(operation.Result["evidence_refs"])...)
		}
	}
	if failedProbe {
		add("evidence_after_failed_probe", postFirstRunEvidence...)
	}
	if r.artifact.FinalState.WorkflowState == workflow.StateResolved && len(remediations) > 0 {
		add("effective_remediation", remediations[len(remediations)-1].ID)
	}
	add("applicability", r.artifact.ScenarioID, r.artifact.Provenance.DatasetVersion)

	fields := make([]string, 0, len(sources))
	result := make(map[string][]string, len(sources))
	for field, values := range sources {
		fields = append(fields, field)
		for value := range values {
			result[field] = append(result[field], value)
		}
		sort.Strings(result[field])
	}
	sort.Strings(fields)
	r.artifact.Experience = IncidentExperience{Fields: fields, Sources: result}
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

func responseEvidenceIDs(value string) []string {
	var response struct {
		EvidenceIDs []string `json:"evidence_ids"`
	}
	if json.Unmarshal([]byte(value), &response) != nil {
		return nil
	}
	seen := make(map[string]struct{}, len(response.EvidenceIDs))
	result := make([]string, 0, len(response.EvidenceIDs))
	for _, id := range response.EvidenceIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func stringsFromAny(value any) []string {
	switch values := value.(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		result := make([]string, 0, len(values))
		for _, item := range values {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
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
	if value.Capabilities == nil {
		value.Capabilities = []string{}
	}
	if value.AgentRuns == nil {
		value.AgentRuns = []AgentRun{}
	}
	if value.ExecutionIntents == nil {
		value.ExecutionIntents = []workflow.ExecutionIntent{}
	}
	if value.WorkflowHistory == nil {
		value.WorkflowHistory = []workflow.Transition{}
	}
	if value.StageFailures == nil {
		value.StageFailures = []workflow.StageFailure{}
	}
	if value.ResolvedActions == nil {
		value.ResolvedActions = []workflow.ResolvedAction{}
	}
	if value.ActionResults == nil {
		value.ActionResults = []workflow.ActionResult{}
	}
	if value.ActionDryRuns == nil {
		value.ActionDryRuns = []workflow.ActionDryRun{}
	}
	if value.ActionPolicies == nil {
		value.ActionPolicies = []workflow.ActionPolicyDecision{}
	}
	if value.ActionApprovals == nil {
		value.ActionApprovals = []workflow.ActionApproval{}
	}
	if value.Checkpoints == nil {
		value.Checkpoints = []workflow.DecisionCheckpoint{}
	}
	if value.BlockedAttempts == nil {
		value.BlockedAttempts = []BlockedAttempt{}
	}
	if value.Operations == nil {
		value.Operations = []platform.Operation{}
	}
	if value.Experience.Fields == nil {
		value.Experience.Fields = []string{}
	}
	if value.Experience.Sources == nil {
		value.Experience.Sources = map[string][]string{}
	}
	for index := range value.AgentRuns {
		if value.AgentRuns[index].ModelCalls == nil {
			value.AgentRuns[index].ModelCalls = []ModelCall{}
		}
		if value.AgentRuns[index].ToolCalls == nil {
			value.AgentRuns[index].ToolCalls = []ToolCall{}
		}
		for callIndex := range value.AgentRuns[index].ToolCalls {
			if value.AgentRuns[index].ToolCalls[callIndex].EvidenceIDs == nil {
				value.AgentRuns[index].ToolCalls[callIndex].EvidenceIDs = []string{}
			}
		}
		if value.AgentRuns[index].ExecutionIntents == nil {
			value.AgentRuns[index].ExecutionIntents = []workflow.ExecutionIntent{}
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

func cloneExecutionIntents(values []workflow.ExecutionIntent) []workflow.ExecutionIntent {
	if values == nil {
		return []workflow.ExecutionIntent{}
	}
	raw, _ := json.Marshal(values)
	var result []workflow.ExecutionIntent
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
