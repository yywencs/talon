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

// Init 初始化全局 observability provider。
func Init(ctx context.Context, cfg Config) error {
	cfg = cfg.Normalize()
	redactor, err := NewRedactor(cfg.RedactionRules)
	if err != nil {
		return fmt.Errorf("create redactor: %w", err)
	}

	globalProvider.mu.Lock()
	defer globalProvider.mu.Unlock()

	if globalProvider.tp != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_ = globalProvider.tp.Shutdown(shutdownCtx)
		globalProvider.tp = nil
	}

	globalProvider.cfg = cfg
	globalProvider.redactor = redactor

	if !cfg.Enabled {
		otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample())))
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
		return nil
	}

	spanProcessors := make([]sdktrace.TracerProviderOption, 0, len(cfg.Exporters)+2)
	for _, kind := range cfg.Exporters {
		exporter, err := buildSpanExporter(ctx, cfg, kind, redactor)
		if err != nil {
			return fmt.Errorf("build exporter %s: %w", kind, err)
		}
		spanProcessors = append(spanProcessors, sdktrace.WithBatcher(exporter))
	}

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

	sampler := sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRate))
	tp := sdktrace.NewTracerProvider(
		append(spanProcessors,
			sdktrace.WithSampler(sampler),
			sdktrace.WithResource(res),
		)...,
	)

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
