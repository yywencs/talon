package logger

import (
	"context"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/wen/opentalon/pkg/config"
)

var LogDir string

func SetupLogger() {
	var handler slog.Handler
	level := slog.LevelInfo
	if config.Global != nil && config.Global.Debug {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: level, AddSource: true}

	if LogDir != "" {
		var logPath string
		if config.Global != nil && config.Global.OneLogFile {
			logPath = filepath.Join(LogDir, "oneapi.log")
		} else {
			logPath = filepath.Join(LogDir, time.Now().Format("oneapi-20060102")+".log")
		}
		fd, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatal("failed to open log file")
		}
		handler = slog.NewJSONHandler(fd, opts)
		slog.SetDefault(slog.New(handler))
		return
	}

	handler = slog.NewTextHandler(os.Stdout, opts)
	slog.SetDefault(slog.New(handler))
}

func Debug(msg string, args ...any) {
	slog.Default().Debug(msg, args...)
}

func Info(msg string, args ...any) {
	slog.Default().Info(msg, args...)
}

func Warn(msg string, args ...any) {
	slog.Default().Warn(msg, args...)
}

func Error(msg string, args ...any) {
	slog.Default().Error(msg, args...)
}

func Fatal(msg string, args ...any) {
	slog.Default().Error(msg, args...)
	os.Exit(1)
}

func DebugWithCtx(ctx context.Context, msg string, args ...any) {
	slog.Default().DebugContext(ctx, msg, args...)
}

func InfoWithCtx(ctx context.Context, msg string, args ...any) {
	slog.Default().InfoContext(ctx, msg, args...)
}

func WarnWithCtx(ctx context.Context, msg string, args ...any) {
	slog.Default().WarnContext(ctx, msg, args...)
}

func ErrorWithCtx(ctx context.Context, msg string, args ...any) {
	slog.Default().ErrorContext(ctx, msg, args...)
}

func FatalWithCtx(ctx context.Context, msg string, args ...any) {
	slog.Default().ErrorContext(ctx, msg, args...)
	os.Exit(1)
}
