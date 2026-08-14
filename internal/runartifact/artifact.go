// Package runartifact records the machine-readable audit trail of one Talon run.
package runartifact

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/wen/opentalon/internal/workflow"
)

const SchemaVersion = "talon.run-artifact/v2"

type Versions struct {
	AgentVersion   string
	DatasetVersion string
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
	ArtifactSchemaVersion string                `json:"artifact_schema_version"`
	AgentVersion          string                `json:"agent_version"`
	DatasetVersion        string                `json:"dataset_version"`
	RunID                 string                `json:"run_id"`
	ScenarioID            string                `json:"scenario_id"`
	StartedAt             time.Time             `json:"started_at"`
	FinishedAt            time.Time             `json:"finished_at"`
	Duration              time.Duration         `json:"duration"`
	Outcome               string                `json:"outcome"`
	StopReason            string                `json:"stop_reason,omitempty"`
	Failure               *Failure              `json:"failure,omitempty"`
	AgentRuns             []AgentRun            `json:"agent_runs"`
	Plans                 []workflow.Plan       `json:"plans"`
	WorkflowHistory       []workflow.Transition `json:"workflow_history"`
	BlockedAttempts       []BlockedAttempt      `json:"blocked_attempts"`
	Summary               Summary               `json:"summary"`
}

type Recorder struct {
	mu               sync.Mutex
	artifact         RunArtifact
	currentRun       int
	currentPlanCount int
	evidence         map[string]struct{}
	now              func() time.Time
}

func New(scenarioID string, versions Versions) *Recorder {
	now := time.Now
	return &Recorder{artifact: RunArtifact{
		ArtifactSchemaVersion: SchemaVersion,
		AgentVersion:          strings.TrimSpace(versions.AgentVersion),
		DatasetVersion:        strings.TrimSpace(versions.DatasetVersion),
		RunID:                 newRunID(),
		ScenarioID:            strings.TrimSpace(scenarioID),
		StartedAt:             now(),
		Outcome:               "running",
		AgentRuns:             []AgentRun{},
		Plans:                 []workflow.Plan{},
		WorkflowHistory:       []workflow.Transition{},
		BlockedAttempts:       []BlockedAttempt{},
	}, evidence: map[string]struct{}{}, now: now}
}

// UnmarshalJSON 兼容已经持久化的 v1 Artifact；v1 使用 schema_version 字段。
func (r *RunArtifact) UnmarshalJSON(data []byte) error {
	type artifactAlias RunArtifact
	value := struct {
		*artifactAlias
		LegacySchemaVersion string `json:"schema_version"`
	}{artifactAlias: (*artifactAlias)(r)}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if r.ArtifactSchemaVersion == "" {
		r.ArtifactSchemaVersion = value.LegacySchemaVersion
	}
	return nil
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
	r.artifact.WorkflowHistory = append([]workflow.Transition(nil), snapshot.History...)
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
	r.artifact.Plans = append([]workflow.Plan(nil), snapshot.Plans...)
	r.artifact.WorkflowHistory = append([]workflow.Transition(nil), snapshot.History...)
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
	return result
}

func clonePlans(values []workflow.Plan) []workflow.Plan {
	raw, _ := json.Marshal(values)
	var result []workflow.Plan
	_ = json.Unmarshal(raw, &result)
	return result
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
