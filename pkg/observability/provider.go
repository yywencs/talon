// Package observability 提供了基于 OpenTelemetry 的链路追踪能力。
// 支持多种导出方式（stdout/jsonl/file/OTLP），可配置采样率和敏感信息脱敏。
// 通过 TracerFor 获取 Tracer 实例，使用 StartSpan 创建追踪区间。

package observability

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	otelattribute "go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type providerState struct {
	mu       sync.RWMutex
	cfg      Config
	tp       *sdktrace.TracerProvider
	redactor *Redactor
}

var globalProvider = &providerState{}

// Init 初始化全局 observability provider。根据 Config 初始化全局状态。
func Init(ctx context.Context, cfg Config) error {
	cfg = cfg.Normalize()

	// 创建敏感信息脱敏器
	redactor, err := NewRedactor(cfg.RedactionRules)
	if err != nil {
		return fmt.Errorf("create redactor: %w", err)
	}

	// 获取互斥锁，确保并发安全
	globalProvider.mu.Lock()
	defer globalProvider.mu.Unlock()

	// 如果已有 Provider，先优雅关闭，避免资源泄漏
	if globalProvider.tp != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_ = globalProvider.tp.Shutdown(shutdownCtx)
		globalProvider.tp = nil
	}

	// 保存配置和脱敏器
	globalProvider.cfg = cfg
	globalProvider.redactor = redactor

	// 未启用时，设置一个永不采样的 Provider，避免性能开销
	if !cfg.Enabled {
		otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample())))
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
		return nil
	}

	// 根据配置创建导出器
	spanProcessors := make([]sdktrace.TracerProviderOption, 0, len(cfg.Exporters)+2)
	for _, kind := range cfg.Exporters {
		exporter, err := buildSpanExporter(ctx, cfg, kind, redactor)
		if err != nil {
			return fmt.Errorf("build exporter %s: %w", kind, err)
		}
		spanProcessors = append(spanProcessors, sdktrace.WithBatcher(exporter))
	}

	// 创建 Resource，包含服务元信息（服务名、版本、环境）
	res, err := resource.New(ctx,
		resource.WithAttributes(
			otelattribute.String("service.name", cfg.ServiceName),
			otelattribute.String("service.version", cfg.ServiceVersion),
			otelattribute.String("deployment.environment", cfg.Environment),
		),
	)
	if err != nil {
		return fmt.Errorf("build resource: %w", err)
	}

	// 配置采样器：基于 TraceID 比例采样，继承父 Span 的采样决策
	sampler := sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRate))
	tp := sdktrace.NewTracerProvider(
		append(spanProcessors,
			sdktrace.WithSampler(sampler),
			sdktrace.WithResource(res),
		)...,
	)

	// 设置全局 Provider 和 Propagator（用于 Context 传播）
	globalProvider.tp = tp
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return nil
}

// Shutdown 关闭全局 observability provider。
func Shutdown(ctx context.Context) error {
	globalProvider.mu.Lock()
	defer globalProvider.mu.Unlock()
	if globalProvider.tp == nil {
		return nil
	}
	err := globalProvider.tp.Shutdown(ctx)
	globalProvider.tp = nil
	return err
}

func currentTracerProvider() *sdktrace.TracerProvider {
	globalProvider.mu.RLock()
	defer globalProvider.mu.RUnlock()
	return globalProvider.tp
}
