package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	otelattribute "go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// ExporterKind 表示导出器类型。
type ExporterKind string

const (
	// ExporterStdout 将 Span 输出到标准输出。
	ExporterStdout ExporterKind = "stdout"
	// ExporterJSONL 将 Span 以 JSONL 形式写入文件。
	ExporterJSONL ExporterKind = "jsonl"
	// ExporterFile 将每个 Span 写入独立 JSON 文件。
	ExporterFile ExporterKind = "file"
	// ExporterOTLP 将 Span 导出到 OTLP 后端。
	ExporterOTLP ExporterKind = "otlp"
)

type exporterFactory interface {
	kind() ExporterKind
	build(ctx context.Context, cfg Config, redactor *Redactor) (sdktrace.SpanExporter, error)
}

type spanRecord struct {
	TraceID           string            `json:"trace_id"`
	SpanID            string            `json:"span_id"`
	ParentSpanID      string            `json:"parent_span_id,omitempty"`
	Name              string            `json:"name"`
	Kind              string            `json:"kind"`
	StartTime         time.Time         `json:"start_time"`
	EndTime           time.Time         `json:"end_time"`
	Status            string            `json:"status"`
	StatusDescription string            `json:"status_description,omitempty"`
	Attributes        map[string]any    `json:"attributes,omitempty"`
	Events            []spanEventRecord `json:"events,omitempty"`
	Resource          map[string]any    `json:"resource,omitempty"`
	Scope             string            `json:"scope,omitempty"`
}

type spanEventRecord struct {
	Name       string         `json:"name"`
	Time       time.Time      `json:"time"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

func buildSpanExporter(ctx context.Context, cfg Config, kind ExporterKind, redactor *Redactor) (sdktrace.SpanExporter, error) {
	var factory exporterFactory
	switch kind {
	case ExporterStdout:
		factory = stdoutExporterFactory{}
	case ExporterJSONL:
		factory = jsonlExporterFactory{}
	case ExporterFile:
		factory = fileExporterFactory{}
	case ExporterOTLP:
		factory = otlpExporterFactory{}
	default:
		return nil, fmt.Errorf("unsupported exporter: %s", kind)
	}
	return factory.build(ctx, cfg, redactor)
}

func parseExporterKinds(value string) []ExporterKind {
	parts := strings.Split(value, ",")
	out := make([]ExporterKind, 0, len(parts))
	seen := make(map[ExporterKind]struct{})
	for _, part := range parts {
		kind := ExporterKind(strings.ToLower(strings.TrimSpace(part)))
		if kind == "" {
			continue
		}
		if _, ok := seen[kind]; ok {
			continue
		}
		seen[kind] = struct{}{}
		out = append(out, kind)
	}
	return out
}

func joinExporterKinds(kinds []ExporterKind) string {
	if len(kinds) == 0 {
		return string(ExporterStdout)
	}
	parts := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		parts = append(parts, string(kind))
	}
	return strings.Join(parts, ",")
}

func spanRecordFromReadOnlySpan(span sdktrace.ReadOnlySpan, redactor *Redactor) spanRecord {
	parentSpanID := ""
	if parent := span.Parent(); parent.IsValid() {
		parentSpanID = parent.SpanID().String()
	}
	return spanRecord{
		TraceID:           span.SpanContext().TraceID().String(),
		SpanID:            span.SpanContext().SpanID().String(),
		ParentSpanID:      parentSpanID,
		Name:              span.Name(),
		Kind:              span.SpanKind().String(),
		StartTime:         span.StartTime(),
		EndTime:           span.EndTime(),
		Status:            spanStatusFromReadOnlySpan(span),
		StatusDescription: span.Status().Description,
		Attributes:        attributeSetToMap(span.Attributes(), redactor),
		Events:            spanEventsToRecords(span.Events(), redactor),
		Resource:          attributeSetToMap(span.Resource().Attributes(), redactor),
		Scope:             span.InstrumentationScope().Name,
	}
}

func spanStatusFromReadOnlySpan(span sdktrace.ReadOnlySpan) string {
	if status, ok := attributeStringValue(span.Attributes(), AttrSpanStatus); ok && status != "" {
		return status
	}
	switch span.Status().Code {
	case otelcodes.Error:
		return string(SpanStatusError)
	case otelcodes.Ok:
		return string(SpanStatusOK)
	default:
		return string(SpanStatusOK)
	}
}

func spanEventsToRecords(events []sdktrace.Event, redactor *Redactor) []spanEventRecord {
	if len(events) == 0 {
		return nil
	}
	out := make([]spanEventRecord, 0, len(events))
	for _, event := range events {
		out = append(out, spanEventRecord{
			Name:       event.Name,
			Time:       event.Time,
			Attributes: attributeSetToMap(event.Attributes, redactor),
		})
	}
	return out
}

func attributeSetToMap(attrs []otelattribute.KeyValue, redactor *Redactor) map[string]any {
	if len(attrs) == 0 {
		return nil
	}
	out := make(map[string]any, len(attrs))
	for _, attr := range attrs {
		value := attributeValueToAny(attr.Value)
		if redactor != nil {
			value = redactor.RedactValue(string(attr.Key), value)
		}
		out[string(attr.Key)] = value
	}
	return out
}

func attributeStringValue(attrs []otelattribute.KeyValue, key string) (string, bool) {
	for _, attr := range attrs {
		if string(attr.Key) != key {
			continue
		}
		value := attributeValueToAny(attr.Value)
		str, ok := value.(string)
		return str, ok
	}
	return "", false
}

func attributeValueToAny(value otelattribute.Value) any {
	switch value.Type() {
	case otelattribute.BOOL:
		return value.AsBool()
	case otelattribute.INT64:
		return value.AsInt64()
	case otelattribute.FLOAT64:
		return value.AsFloat64()
	case otelattribute.STRING:
		return value.AsString()
	case otelattribute.BOOLSLICE:
		return value.AsBoolSlice()
	case otelattribute.INT64SLICE:
		return value.AsInt64Slice()
	case otelattribute.FLOAT64SLICE:
		return value.AsFloat64Slice()
	case otelattribute.STRINGSLICE:
		return value.AsStringSlice()
	default:
		return value.Emit()
	}
}

func marshalSpanRecord(record spanRecord) ([]byte, error) {
	return json.Marshal(record)
}
