package sandbox

import "errors"

// ErrRuntimeNotImplemented 表示当前 sandbox runtime 尚未实现。
var ErrRuntimeNotImplemented = errors.New("sandbox runtime is not implemented yet")

// ErrSandboxNotRunning 表示 sandbox 尚未启动，无法执行命令。
var ErrSandboxNotRunning = errors.New("sandbox is not running")

// ErrSandboxClosed 表示 sandbox 已关闭，无法再次执行。
var ErrSandboxClosed = errors.New("sandbox is closed")

// ErrSandboxTimedOut 表示 sandbox 执行超时。
var ErrSandboxTimedOut = errors.New("sandbox execution timed out")

// ErrSandboxCancelled 表示 sandbox 执行被取消。
var ErrSandboxCancelled = errors.New("sandbox execution cancelled")

// ErrSandboxResourceLimitExceeded 表示 sandbox 命中了资源限制。
var ErrSandboxResourceLimitExceeded = errors.New("sandbox resource limit exceeded")

// ErrSandboxRecovered 表示 sandbox 检测到异常状态并已完成恢复。
var ErrSandboxRecovered = errors.New("sandbox recovered from unhealthy state")
