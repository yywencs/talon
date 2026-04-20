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

type fileExporterFactory struct{}

func (fileExporterFactory) kind() ExporterKind {
	return ExporterFile
}

func (fileExporterFactory) build(ctx context.Context, cfg Config, redactor *Redactor) (sdktrace.SpanExporter, error) {
	_ = ctx
	dir := filepath.Join(cfg.TraceDir, "spans")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create span dir: %w", err)
	}
	return &fileSpanExporter{dir: dir, redactor: redactor}, nil
}

type fileSpanExporter struct {
	mu       sync.Mutex
	dir      string
	redactor *Redactor
}

func (e *fileSpanExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	_ = ctx
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, span := range spans {
		record := spanRecordFromReadOnlySpan(span, e.redactor)
		payload, err := marshalSpanRecord(record)
		if err != nil {
			return err
		}
		fileName := fmt.Sprintf("%s-%s-%d.json", record.TraceID, record.SpanID, time.Now().UnixNano())
		filePath := filepath.Join(e.dir, fileName)
		if err := os.WriteFile(filePath, payload, 0644); err != nil {
			return err
		}
	}
	return nil
}

func (e *fileSpanExporter) Shutdown(ctx context.Context) error {
	_ = ctx
	return nil
}
