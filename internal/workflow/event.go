package workflow

// EventType 表示导致 Incident 状态变化的领域事件。
type EventType string

const (
	// EventStartInvestigation 表示控制器已完成保护动作，可以开始调查。
	EventStartInvestigation EventType = "start_investigation"
	// EventExecutionIntentSubmitted 表示 Agent 已提交一份结构化 ExecutionIntent。
	EventExecutionIntentSubmitted EventType = "execution_intent_submitted"
	// EventSkillLoaded 表示 Agent 基于公开证据按需加载了一个诊断 Skill。
	EventSkillLoaded EventType = "skill_loaded"
	// EventSkillUnloaded 表示新证据否定原假设后，Agent 卸载了一个诊断 Skill。
	EventSkillUnloaded EventType = "skill_unloaded"
	// EventApprovalRequired 表示 Workflow 判定 ExecutionIntent 必须等待人工审批。
	EventApprovalRequired EventType = "approval_required"
	// EventExecutionAuthorized 表示 ExecutionIntent 已通过自动 Policy 或人工审批。
	EventExecutionAuthorized EventType = "execution_authorized"
	// EventExecutionIntentRejected 表示 ExecutionIntent 被 Policy 或人工拒绝，需要 Agent 基于当前证据重新决策。
	EventExecutionIntentRejected EventType = "execution_intent_rejected"
	// EventStageCheckpoint 表示动态 Stage 已执行完成，必须先做确定性决策。
	EventStageCheckpoint EventType = "stage_checkpoint"
	// EventCheckpointContinue 表示检查点判定“继续”：当前 Stage 成功且存在下一个
	// 线性 Stage，回到 validating 重新走 Dry Run 与策略评估。
	EventCheckpointContinue EventType = "checkpoint_continue"
	// EventCheckpointNeedsAgent 表示检查点要求把控制权交回 Agent：携带结构化失败
	// 上下文回到 investigating 重新决策，每次消耗一次 agent_resumes 限额。
	EventCheckpointNeedsAgent EventType = "checkpoint_needs_agent"
	// EventCheckpointSucceeded 表示检查点判定整个 Intent 成功完成，进入最终终态 resolved。
	EventCheckpointSucceeded EventType = "checkpoint_succeeded"
	// EventCheckpointFailed 表示检查点确定性判定失败（如重试失败收敛、Stage 数或
	// Agent 唤回限额耗尽），进入终态 failed。
	EventCheckpointFailed EventType = "checkpoint_failed"
	// EventCheckpointEscalated 表示检查点判定应停止自治、携带证据交人工接管，
	// 进入暂停终态 escalated。
	EventCheckpointEscalated EventType = "checkpoint_escalated"
	// EventCheckpointBlocked 表示检查点判定当前路径被授权/审批/策略拒绝，无法
	// 继续自主执行，进入终态 blocked。
	EventCheckpointBlocked EventType = "checkpoint_blocked"
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
