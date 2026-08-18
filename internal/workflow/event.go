package workflow

// EventType 表示导致 Incident 状态变化的领域事件。
type EventType string

const (
	// EventStartInvestigation 表示控制器已完成保护动作，可以开始调查。
	EventStartInvestigation EventType = "start_investigation"
	// EventPlanSubmitted 表示 Agent 已提交一份结构化 Plan。
	EventPlanSubmitted EventType = "plan_submitted"
	// EventSkillLoaded 表示 Agent 基于公开证据按需加载了一个诊断 Skill。
	EventSkillLoaded EventType = "skill_loaded"
	// EventSkillUnloaded 表示新证据否定原假设后，Agent 卸载了一个诊断 Skill。
	EventSkillUnloaded EventType = "skill_unloaded"
	// EventApprovalRequired 表示 Workflow 判定 Plan 必须等待人工审批。
	EventApprovalRequired EventType = "approval_required"
	// EventPlanApproved 表示 Plan 已通过自动 Policy 或人工审批。
	EventPlanApproved EventType = "plan_approved"
	// EventPlanRejected 表示 Plan 被 Policy 或人工拒绝，需要重新调查。
	EventPlanRejected EventType = "plan_rejected"
	// EventStageSucceeded 表示当前修复、探测、恢复或补偿阶段已成功完成。
	EventStageSucceeded EventType = "stage_succeeded"
	// EventStageFailed 表示当前阶段失败或被停止，失败原因记录在 Reason/Metadata 中。
	EventStageFailed EventType = "stage_failed"
	// EventCompensationRequired 表示当前状态不能直接重试，必须先执行补偿动作。
	EventCompensationRequired EventType = "compensation_required"
	// EventEscalated 表示事件停止自治并交由人工处理。
	EventEscalated EventType = "escalated"
	// EventHumanResumed 表示人工明确授权 Workflow 恢复调查。
	EventHumanResumed EventType = "human_resumed"
)

// Event 是提交给状态机的不可变事实。Reason 和 Metadata 只用于审计；
// Failure 保存确定性失败语义，三者都不能扩大 Actor 权限。
type Event struct {
	Type     EventType         `json:"type"`
	Actor    Actor             `json:"actor"`
	Reason   string            `json:"reason,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Failure  *StageFailure     `json:"failure,omitempty"`
}
