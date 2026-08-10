package observability

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type jsonlExporterFactory struct{}

func (jsonlExporterFactory) kind() ExporterKind {
	return ExporterJSONL
}

func (jsonlExporterFactory) build(ctx context.Context, cfg Config, redactor *Redactor, dirManager *traceDirectoryManager) (sdktrace.SpanExporter, error) {
	_ = ctx
	if dirManager == nil {
		var err error
		dirManager, err = newTraceDirectoryManager(cfg.TraceDir)
		if err != nil {
			return nil, fmt.Errorf("create jsonl exporter directories: %w", err)
		}
	}
	return &jsonlSpanExporter{
		dirManager: dirManager,
		files:      make(map[string]*os.File),
		records:    make(map[string]map[string]spanRecord),
		redactor:   redactor,
	}, nil
}

type jsonlSpanExporter struct {
	mu         sync.Mutex
	dirManager *traceDirectoryManager
	files      map[string]*os.File
	records    map[string]map[string]spanRecord
	redactor   *Redactor
}

func (e *jsonlSpanExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	_ = ctx
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, span := range spans {
		record := spanRecordFromReadOnlySpan(span, e.redactor)
		payload, err := marshalSpanRecordJSONL(record)
		if err != nil {
			return err
		}
		traceDir, err := e.dirManager.TraceDir(record)
		if err != nil {
			return err
		}
		fd, err := e.fileForTraceDir(traceDir)
		if err != nil {
			return err
		}
		if _, err := fd.Write(append(payload, '\n')); err != nil {
			return err
		}
		e.upsertRecord(traceDir, record)
		if err := writeTraceArtifacts(traceDir, sortedTraceRecords(e.records[traceDir])); err != nil {
			return err
		}
	}
	return nil
}

func (e *jsonlSpanExporter) Shutdown(ctx context.Context) error {
	_ = ctx
	e.mu.Lock()
	defer e.mu.Unlock()
	var firstErr error
	for traceDir, fd := range e.files {
		if fd == nil {
			continue
		}
		if err := fd.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close jsonl file for %s: %w", traceDir, err)
		}
	}
	e.files = make(map[string]*os.File)
	e.records = make(map[string]map[string]spanRecord)
	return firstErr
}

func (e *jsonlSpanExporter) fileForTraceDir(traceDir string) (*os.File, error) {
	if fd, ok := e.files[traceDir]; ok && fd != nil {
		return fd, nil
	}
	filePath := filepath.Join(traceDir, "spans.jsonl")
	fd, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open jsonl trace file: %w", err)
	}
	e.files[traceDir] = fd
	return fd, nil
}

func (e *jsonlSpanExporter) upsertRecord(traceDir string, record spanRecord) {
	if e.records == nil {
		e.records = make(map[string]map[string]spanRecord)
	}
	if _, ok := e.records[traceDir]; !ok {
		e.records[traceDir] = make(map[string]spanRecord)
	}
	e.records[traceDir][record.SpanID] = record
}
