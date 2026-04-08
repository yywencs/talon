package types

// Observation 接口由所有观察结果类型实现，表示环境对 Action 的响应。
// 与 Action 的本质区别：Observation 是"被动产生"的，其 Cause 字段必须指向对应的 Action ID。
// 如果 Observation 回来时没有匹配的 PendingAction，系统会忽略它（不会错误解锁）。
type Observation interface {
	Event
	isObservation()
}

type ObservationType string

const (
	ObsRun     ObservationType = "run"
	ObsRead    ObservationType = "read"
	ObsWrite   ObservationType = "write"
	ObsEdit    ObservationType = "edit"
	ObsError   ObservationType = "error"
	ObsSuccess ObservationType = "success"
)

// CmdOutputObservation 是命令执行结果的包装器。
// 包含命令的退出码（ExitCode），使得 Agent 可以区分"命令成功"和"命令失败但有输出"两种情况。
// Content 是原始 stdout/stderr 混合输出；Agent 可以自行解析其格式。
type CmdOutputObservation struct {
	BaseEvent
	Content  string `json:"content"`
	ExitCode int    `json:"exit_code,omitempty"`
}

func (e *CmdOutputObservation) GetBase() *BaseEvent { return &e.BaseEvent }
func (e *CmdOutputObservation) Kind() EventKind     { return KindObservation }
func (e *CmdOutputObservation) Name() string        { return "CmdOutput" }
func (e *CmdOutputObservation) isObservation()      {}
