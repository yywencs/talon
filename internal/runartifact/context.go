package runartifact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// IncidentContextSchemaVersion 表示 Incident 上下文序列化结构的版本。
// 消费方应使用该版本识别不兼容的快照结构。
const IncidentContextSchemaVersion = "talon.incident-context/v1"

// IncidentContextSnapshot 表示每轮 Agent 运行前生成的、大小受限且模型可见的状态快照。
// 同一份快照也会持久化到 RunArtifact，供运行回放和评测使用。
type IncidentContextSnapshot struct {
	SchemaVersion    string                        `json:"schema_version"`
	Digest           string                        `json:"digest"`
	GeneratedAt      time.Time                     `json:"generated_at"`
	VirtualTime      time.Time                     `json:"virtual_time,omitempty"`
	IncidentID       string                        `json:"incident_id"`
	Objective        string                        `json:"objective"`
	Workflow         IncidentContextWorkflow       `json:"workflow"`
	ActiveSkills     []IncidentContextSkill        `json:"active_skills"`
	Budget           IncidentContextBudget         `json:"budget"`
	Evidence         []IncidentContextEvidence     `json:"evidence"`
	Plans            []IncidentContextPlan         `json:"plans"`
	ActionResults    []IncidentContextActionResult `json:"action_results,omitempty"`
	LatestCheckpoint *IncidentContextCheckpoint    `json:"latest_checkpoint,omitempty"`
	LatestFailure    *IncidentContextFailure       `json:"latest_failure,omitempty"`
	Constraints      []string                      `json:"constraints"`
}

// IncidentContextWorkflow 描述当前工作流状态，以及该状态下 Agent 可以请求的操作。
type IncidentContextWorkflow struct {
	State          string   `json:"state"`
	SuspendedState string   `json:"suspended_state,omitempty"`
	Version        uint64   `json:"version"`
	AllowedActions []string `json:"allowed_actions"`
}

// IncidentContextSkill 标识一个已启用的 Skill 及其内容摘要。
type IncidentContextSkill struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// IncidentContextBudget 汇总当前 Agent 运行前的资源用量及本轮适用的限制。
type IncidentContextBudget struct {
	AgentRunSequence    int        `json:"agent_run_sequence"`
	AgentRunsUsed       int        `json:"agent_runs_used"`
	ModelCallsUsed      int        `json:"model_calls_used"`
	ToolCallsUsed       int        `json:"tool_calls_used"`
	TotalTokensUsed     int        `json:"total_tokens_used"`
	MaxStepsThisRun     int        `json:"max_steps_this_run"`
	MaxModelCalls       int        `json:"max_model_calls,omitempty"`
	ModelCallsRemaining int        `json:"model_calls_remaining,omitempty"`
	DeadlineAt          *time.Time `json:"deadline_at,omitempty"`
	RemainingMillis     int64      `json:"remaining_ms,omitempty"`
}

// IncidentContextEvidence 指向成功的只读工具调用所产生的证据。
// 为避免把原始工具输出直接写入上下文，这里只保存证据引用和标识符。
type IncidentContextEvidence struct {
	EvidenceRef string    `json:"evidence_ref"`
	EvidenceIDs []string  `json:"evidence_ids"`
	SourceTool  string    `json:"source_tool"`
	AgentRun    int       `json:"agent_run"`
	ObservedAt  time.Time `json:"observed_at"`
}

// IncidentContextPlan 汇总此前提交的 Plan，以及生成快照时观察到的执行结果。
type IncidentContextPlan struct {
	ID           string   `json:"id"`
	Summary      string   `json:"summary"`
	RootCause    string   `json:"root_cause"`
	EvidenceRefs []string `json:"evidence_refs"`
	Actions      []string `json:"actions"`
	Outcome      string   `json:"outcome"`
}

// IncidentContextActionResult 是模型可见的、大小受限的 Harness 工具观察。
// Output 仍被明确标记为外部数据，不具备指令权限。
type IncidentContextActionResult struct {
	EvidenceRef     string         `json:"evidence_ref"`
	StageID         string         `json:"stage_id"`
	ActionID        string         `json:"action_id"`
	OperationID     string         `json:"operation_id"`
	OperationStatus string         `json:"operation_status"`
	Output          map[string]any `json:"output"`
	ObservedAt      time.Time      `json:"observed_at"`
}

type IncidentContextCheckpoint struct {
	CheckpointID   string `json:"checkpoint_id"`
	StageID        string `json:"stage_id"`
	Decision       string `json:"decision"`
	DecisionReason string `json:"decision_reason"`
	NextStageID    string `json:"next_stage_id,omitempty"`
}

// IncidentContextFailure 保存经过规范化的失败信息，用于指导下一轮调查，
// 同时避免向模型暴露不可信的原始失败文本。
type IncidentContextFailure struct {
	Event       string            `json:"event"`
	Stage       string            `json:"stage,omitempty"`
	Reason      string            `json:"reason,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Category    string            `json:"category,omitempty"`
	Code        string            `json:"code,omitempty"`
	NextAction  string            `json:"next_action,omitempty"`
	Retryable   bool              `json:"retryable,omitempty"`
	Fallback    bool              `json:"fallback,omitempty"`
	OperationID string            `json:"operation_id,omitempty"`
	ActionID    string            `json:"action_id,omitempty"`
}

// SealIncidentContextSnapshot 对快照进行规范化，设置当前结构版本，并计算稳定的内容摘要。
// 摘要计算不包含 GeneratedAt，因此内容相同的上下文不会因生成时间不同而产生不同摘要。
func SealIncidentContextSnapshot(value IncidentContextSnapshot) IncidentContextSnapshot {
	value.SchemaVersion = IncidentContextSchemaVersion
	if value.GeneratedAt.IsZero() {
		value.GeneratedAt = time.Now()
	}
	if value.ActiveSkills == nil {
		value.ActiveSkills = []IncidentContextSkill{}
	}
	if value.Workflow.AllowedActions == nil {
		value.Workflow.AllowedActions = []string{}
	}
	if value.Evidence == nil {
		value.Evidence = []IncidentContextEvidence{}
	}
	for index := range value.Evidence {
		if value.Evidence[index].EvidenceIDs == nil {
			value.Evidence[index].EvidenceIDs = []string{}
		}
	}
	if value.Plans == nil {
		value.Plans = []IncidentContextPlan{}
	}
	if value.ActionResults == nil {
		value.ActionResults = []IncidentContextActionResult{}
	}
	for index := range value.ActionResults {
		if value.ActionResults[index].Output == nil {
			value.ActionResults[index].Output = map[string]any{}
		}
	}
	for index := range value.Plans {
		if value.Plans[index].EvidenceRefs == nil {
			value.Plans[index].EvidenceRefs = []string{}
		}
		if value.Plans[index].Actions == nil {
			value.Plans[index].Actions = []string{}
		}
	}
	if value.Constraints == nil {
		value.Constraints = []string{}
	}
	value.Digest = ""
	digestValue := value
	digestValue.GeneratedAt = time.Time{}
	payload, _ := json.Marshal(digestValue)
	sum := sha256.Sum256(payload)
	value.Digest = "sha256:" + hex.EncodeToString(sum[:])
	return value
}
