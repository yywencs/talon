// Package observability 使用 CozeLoop 记录 ToolOps Agent、Eino Graph、模型和工具调用。
package observability

import (
	"context"
	"fmt"
	"sync"

	loopcallback "github.com/cloudwego/eino-ext/callbacks/cozeloop"
	"github.com/cloudwego/eino/callbacks"
	cozeloop "github.com/coze-dev/cozeloop-go"
)

type providerState struct {
	mu       sync.RWMutex
	cfg      Config
	client   cozeloop.Client
	handler  callbacks.Handler
	redactor *Redactor
}

var globalProvider providerState

// Init 初始化 CozeLoop Client 和 Eino 官方 Callback。
func Init(_ context.Context, cfg Config) error {
	cfg = cfg.Normalize()
	redactor, err := NewRedactor(cfg.RedactionRules)
	if err != nil {
		return fmt.Errorf("create CozeLoop redactor: %w", err)
	}
	if cfg.Enabled {
		if cfg.WorkspaceID == "" {
			return fmt.Errorf("%s is required when CozeLoop is enabled", envWorkspaceID)
		}
		if !cfg.hasAuthentication() {
			return fmt.Errorf("CozeLoop requires %s or complete JWT OAuth credentials", envAPIToken)
		}
	}

	globalProvider.mu.Lock()
	defer globalProvider.mu.Unlock()
	if globalProvider.client != nil {
		return fmt.Errorf("CozeLoop observability is already initialized")
	}
	globalProvider.cfg = cfg
	globalProvider.redactor = redactor
	if !cfg.Enabled {
		return nil
	}

	options := []cozeloop.Option{
		cozeloop.WithWorkspaceID(cfg.WorkspaceID),
		cozeloop.WithTraceTagTruncateConf(&cozeloop.TagTruncateConf{
			NormalFieldMaxByte:      cfg.NormalMaxBytes,
			InputOutputFieldMaxByte: cfg.InputOutputMaxBytes,
		}),
	}
	if cfg.APIBaseURL != "" {
		options = append(options, cozeloop.WithAPIBaseURL(cfg.APIBaseURL))
	}
	if cfg.APIToken != "" {
		options = append(options, cozeloop.WithAPIToken(cfg.APIToken))
	} else {
		options = append(options,
			cozeloop.WithJWTOAuthClientID(cfg.JWTClientID),
			cozeloop.WithJWTOAuthPrivateKey(cfg.JWTPrivateKey),
			cozeloop.WithJWTOAuthPublicKeyID(cfg.JWTPublicKeyID),
		)
	}
	client, err := cozeloop.NewClient(options...)
	if err != nil {
		return fmt.Errorf("create CozeLoop client: %w", err)
	}
	parser := newSafeDataParser(loopcallback.NewDefaultDataParser(cfg.AggregateOutput), redactor)
	handler := loopcallback.NewLoopHandler(client,
		loopcallback.WithCallbackDataParser(parser),
		loopcallback.WithAggrMessageOutput(cfg.AggregateOutput),
	)
	globalProvider.client = client
	globalProvider.handler = newSafeEinoHandler(handler, redactor)
	return nil
}

// Shutdown 强制上报队列中的 Span 并关闭 CozeLoop Client。
func Shutdown(ctx context.Context) error {
	globalProvider.mu.Lock()
	client := globalProvider.client
	globalProvider.client = nil
	globalProvider.handler = nil
	globalProvider.redactor = nil
	globalProvider.cfg = Config{}
	globalProvider.mu.Unlock()
	if client == nil {
		return nil
	}
	client.Flush(ctx)
	client.Close(ctx)
	return ctx.Err()
}

// EinoHandler 返回当前 CozeLoop Eino Callback；未启用时返回 nil。
func EinoHandler() callbacks.Handler {
	globalProvider.mu.RLock()
	defer globalProvider.mu.RUnlock()
	return globalProvider.handler
}

func providerSnapshot() (cozeloop.Client, Config, *Redactor) {
	globalProvider.mu.RLock()
	defer globalProvider.mu.RUnlock()
	return globalProvider.client, globalProvider.cfg, globalProvider.redactor
}
