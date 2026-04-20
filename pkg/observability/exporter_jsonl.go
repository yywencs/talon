package observability

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type jsonlExporterFactory struct{}

func (jsonlExporterFactory) kind() ExporterKind {
	return ExporterJSONL
}

func (jsonlExporterFactory) build(ctx context.Context, cfg Config, redactor *Redactor) (sdktrace.SpanExporter, error) {
	_ = ctx
	if err := os.MkdirAll(cfg.TraceDir, 0755); err != nil {
		return nil, fmt.Errorf("create trace dir: %w", err)
	}
	filePath := filepath.Join(cfg.TraceDir, time.Now().Format("20060102")+".jsonl")
	fd, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open jsonl trace file: %w", err)
	}
	return &jsonlSpanExporter{fd: fd, redactor: redactor}, nil
}

type jsonlSpanExporter struct {
	mu       sync.Mutex
	fd       *os.File
	redactor *Redactor
}

func (e *jsonlSpanExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	_ = ctx
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, span := range spans {
		record := spanRecordFromReadOnlySpan(span, e.redactor)
		payload, err := marshalSpanRecord(record)
		if err != nil {
			return err
		}
		if _, err := e.fd.Write(append(payload, '\n')); err != nil {
			return err
		}
	}
	return nil
}

func (e *jsonlSpanExporter) Shutdown(ctx context.Context) error {
	_ = ctx
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fd == nil {
		return nil
	}
	err := e.fd.Close()
	e.fd = nil
	return err
}
