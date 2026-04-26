package terminal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/wen/opentalon/pkg/logger"
)

func auditCommandName(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func auditCommandHash(command string) string {
	sum := sha256.Sum256([]byte(command))
	return hex.EncodeToString(sum[:])
}

func logTerminalCommandCompletion(ctx context.Context, action BashTool, workingDir string, timeout float64, result commandResult) {
	args := []any{
		"tool_name", "bash",
		"command_name", auditCommandName(action.Command),
		"command_sha256", auditCommandHash(action.Command),
		"working_dir", workingDir,
		"timeout_secs", timeout,
		"security_risk", string(action.SecurityRisk),
		"exit_code", result.ExitCode,
		"timed_out", result.TimedOut,
		"output_truncated", result.OutputTruncated,
		"output_size", len(result.Output),
	}
	if result.PID != nil {
		args = append(args, "pid", *result.PID)
	}
	if result.TimedOut {
		logger.WarnWithCtx(ctx, "审计: bash 命令执行超时", args...)
		return
	}
	if result.ExitCode != 0 {
		logger.WarnWithCtx(ctx, "审计: bash 命令执行异常结束", args...)
		return
	}
	logger.InfoWithCtx(ctx, "审计: bash 命令执行完成", args...)
}
