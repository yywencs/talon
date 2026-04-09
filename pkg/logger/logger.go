package logger

import (
	"context"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/wen/opentalon/pkg/config"
)

var LogDir string

type SimpleJSONHandler struct {
	slog.JSONHandler
}

func (h *SimpleJSONHandler) Handle(ctx context.Context, r slog.Record) error {
	r.Time = r.Time.Truncate(time.Second)
	return h.JSONHandler.Handle(ctx, r)
}

func getCaller() (string, int) {
	pc, file, line, ok := runtime.Caller(2)
	if !ok {
		return "unknown", 0
	}
	fn := runtime.FuncForPC(pc)
	funcName := ""
	if fn != nil {
		funcName = fn.Name()
		parts := strings.Split(funcName, ".")
		if len(parts) > 0 {
			funcName = parts[len(parts)-1]
		}
	}
	shortFile := filepath.Base(file)
	return shortFile + ":" + funcName, line
}

func SetupLogger() {
	var handler slog.Handler
	level := slog.LevelInfo
	if config.Global != nil && config.Global.Debug {
		level = slog.LevelDebug
	}

	opts := &slog.HandlerOptions{
		Level:     level,
		AddSource: false,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				a.Value = slog.StringValue(a.Value.Time().Format("15:04:05"))
			}
			if a.Key == slog.SourceKey {
				pc, file, line, ok := runtime.Caller(4)
				if !ok {
					return slog.Attr{Key: "source", Value: slog.StringValue("???")}
				}
				fn := runtime.FuncForPC(pc)
				funcName := ""
				if fn != nil {
					funcName = fn.Name()
					parts := strings.Split(funcName, ".")
					if len(parts) > 0 {
						funcName = parts[len(parts)-1]
					}
				}
				shortFile := filepath.Base(file)
				a.Value = slog.StringValue(shortFile + ":" + funcName + ":" + strconv.Itoa(line))
			}
			return a
		},
	}

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
