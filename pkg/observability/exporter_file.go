package observability

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type fileExporterFactory struct{}

func (fileExporterFactory) kind() ExporterKind {
	return ExporterFile
}

func (fileExporterFactory) build(ctx context.Context, cfg Config, redactor *Redactor) (sdktrace.SpanExporter, error) {
	_ = ctx
	dirManager, err := newTraceDirectoryManager(cfg.TraceDir)
	if err != nil {
		return nil, fmt.Errorf("create file exporter directories: %w", err)
	}
	return &fileSpanExporter{
		dirManager: dirManager,
		records:    make(map[string]map[string]spanRecord),
		redactor:   redactor,
	}, nil
}

type fileSpanExporter struct {
	mu         sync.Mutex
	dirManager *traceDirectoryManager
	records    map[string]map[string]spanRecord
	redactor   *Redactor
}

func (e *fileSpanExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	_ = ctx
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, span := range spans {
		record := spanRecordFromReadOnlySpan(span, e.redactor)
		payload, err := marshalSpanRecordJSON(record)
		if err != nil {
			return err
		}
		traceDir, err := e.dirManager.TraceDir(record)
		if err != nil {
			return err
		}
		filePath := filepath.Join(traceDir, buildSpanFileName(record))
		if err := os.WriteFile(filePath, payload, 0644); err != nil {
			return err
		}
		e.upsertRecord(traceDir, record)
		if err := writeTraceArtifacts(traceDir, sortedTraceRecords(e.records[traceDir])); err != nil {
			return err
		}
	}
	return nil
}

func (e *fileSpanExporter) Shutdown(ctx context.Context) error {
	_ = ctx
	return nil
}

func (e *fileSpanExporter) upsertRecord(traceDir string, record spanRecord) {
	if e.records == nil {
		e.records = make(map[string]map[string]spanRecord)
	}
	if _, ok := e.records[traceDir]; !ok {
		e.records[traceDir] = make(map[string]spanRecord)
	}
	e.records[traceDir][record.SpanID] = record
}
