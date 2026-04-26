package observability

import (
	"context"
	"fmt"
	"strings"

	otlptracehttp "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type otlpExporterFactory struct{}

func (otlpExporterFactory) kind() ExporterKind {
	return ExporterOTLP
}

func (otlpExporterFactory) build(ctx context.Context, cfg Config, redactor *Redactor, dirManager *traceDirectoryManager) (sdktrace.SpanExporter, error) {
	_ = redactor
	_ = dirManager
	endpoint := strings.TrimSpace(cfg.OTLPEndpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("otlp exporter requires OBS_OTLP_ENDPOINT")
	}
	options := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(endpoint),
	}
	if cfg.OTLPInsecure {
		options = append(options, otlptracehttp.WithInsecure())
	}
	return otlptracehttp.New(ctx, options...)
}
