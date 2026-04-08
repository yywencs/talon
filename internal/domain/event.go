package domain

type EventType string

const (
	EventUserTaskInput EventType = "USER_TASK_INPUT" // 用户输入了新任务
	EventThinkStart    EventType = "THINK_START"     // 触发大模型思考
	EventThinkDone     EventType = "THINK_DONE"      // 大模型思考完毕
	EventToolCall      EventType = "TOOL_CALL"       // 大模型决定调用工具
	EventToolResult    EventType = "TOOL_RESULT"     // 工具执行返回了结果
	EventTaskFinish    EventType = "TASK_FINISH"     // 任务最终完成
	EventError         EventType = "ERROR"           // 发生异常
)

// Event 是在通道里流转的数据包
type Event struct {
	Type    EventType
	Payload interface{} // 灵活携带数据：大模型的回复、工具的 JSON 结果等
}

// AgentState 是状态机的“记忆” (持久化快照的核心)
type AgentState struct {
	SessionID string
	Status    string
	Memory    []string // 极其重要：存放对话上下文和工具执行历史
}
