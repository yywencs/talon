package observability

import (
	"context"

	stdouttrace "go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type stdoutExporterFactory struct{}

func (stdoutExporterFactory) kind() ExporterKind {
	return ExporterStdout
}

func (stdoutExporterFactory) build(ctx context.Context, cfg Config, redactor *Redactor) (sdktrace.SpanExporter, error) {
	_ = ctx
	_ = redactor
	options := []stdouttrace.Option{
		stdouttrace.WithoutTimestamps(),
	}
	if cfg.StdoutPretty {
		options = append(options, stdouttrace.WithPrettyPrint())
	}
	return stdouttrace.New(options...)
}
