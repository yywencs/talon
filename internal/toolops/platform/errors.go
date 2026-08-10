package platform

import "errors"

var (
	// ErrNotFound 表示请求的资源不存在。
	ErrNotFound = errors.New("toolops resource not found")
	// ErrUnauthorized 表示调用方没有执行该动作的权限。
	ErrUnauthorized = errors.New("toolops action unauthorized")
	// ErrConflict 表示 expected version、并发动作或幂等键发生冲突。
	ErrConflict = errors.New("toolops state conflict")
	// ErrPreconditionFailed 表示动作的安全前置条件尚未满足。
	ErrPreconditionFailed = errors.New("toolops precondition failed")
	// ErrUnsupported 表示当前 Adapter 不支持所请求的能力。
	ErrUnsupported = errors.New("toolops capability unsupported")
)
