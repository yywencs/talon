package sandbox

import "errors"

// ErrRuntimeNotImplemented 表示当前 sandbox runtime 尚未实现。
var ErrRuntimeNotImplemented = errors.New("sandbox runtime is not implemented yet")

// ErrSandboxNotRunning 表示 sandbox 尚未启动，无法执行命令。
var ErrSandboxNotRunning = errors.New("sandbox is not running")

// ErrSandboxClosed 表示 sandbox 已关闭，无法再次执行。
var ErrSandboxClosed = errors.New("sandbox is closed")
