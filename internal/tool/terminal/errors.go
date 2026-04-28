package terminal

import (
	"errors"
	"fmt"
)

// TerminalToolError 表示终端工具的基础错误类型。
type TerminalToolError struct {
	Message string
	Hint    string
	Cause   error
}

// Error 返回错误文本。
func (e *TerminalToolError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Unwrap 返回底层错误，便于错误链分析。
func (e *TerminalToolError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// TerminalStateError 表示终端会话或 pending 状态异常。
type TerminalStateError struct {
	TerminalToolError
	Reason string
}

// TerminalBackendOperationError 表示终端后端操作失败。
type TerminalBackendOperationError struct {
	TerminalToolError
	Operation string
	Fatal     bool
}

// TerminalExecutionError 表示终端执行流程中的稳定错误。
type TerminalExecutionError struct {
	TerminalToolError
}

// BuildTerminalErrorMessage 根据错误类型生成稳定的错误文本。
func BuildTerminalErrorMessage(err error) string {
	if err == nil {
		return "未知终端错误"
	}

	var stateErr *TerminalStateError
	if errors.As(err, &stateErr) {
		return formatTerminalErrorMessage(stateErr.Message, stateErr.Hint)
	}

	var backendErr *TerminalBackendOperationError
	if errors.As(err, &backendErr) {
		return formatTerminalErrorMessage(backendErr.Message, backendErr.Hint)
	}

	var execErr *TerminalExecutionError
	if errors.As(err, &execErr) {
		return formatTerminalErrorMessage(execErr.Message, execErr.Hint)
	}

	return err.Error()
}

// NewTerminalStateError 构造终端状态错误。
func NewTerminalStateError(reason, hint string) error {
	return &TerminalStateError{
		TerminalToolError: TerminalToolError{
			Message: reason,
			Hint:    hint,
		},
		Reason: reason,
	}
}

// NewTerminalBackendOperationError 构造终端后端操作错误。
func NewTerminalBackendOperationError(operation string, cause error, hint string) error {
	return &TerminalBackendOperationError{
		TerminalToolError: TerminalToolError{
			Message: fmt.Sprintf("%s失败: %v", operation, cause),
			Hint:    hint,
			Cause:   cause,
		},
		Operation: operation,
		Fatal:     false,
	}
}

// NewTerminalExecutionError 构造终端执行错误。
func NewTerminalExecutionError(message string, cause error, hint string) error {
	return &TerminalExecutionError{
		TerminalToolError: TerminalToolError{
			Message: message,
			Hint:    hint,
			Cause:   cause,
		},
	}
}

func formatTerminalErrorMessage(message, hint string) string {
	if hint == "" {
		return message
	}
	return message + "\n提示: " + hint
}
