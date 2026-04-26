// Package observability 提供了基于 OpenTelemetry 的链路追踪能力。
// 支持多种导出方式（stdout/jsonl/file/OTLP），可配置采样率和敏感信息脱敏。
// 通过 TracerFor 获取 Tracer 实例，使用 StartSpan 创建追踪区间。

package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	otelattribute "go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// ExporterKind 表示导出器类型。
type ExporterKind string

const (
	ExporterStdout ExporterKind = "stdout" // 标准输出
	ExporterJSONL  ExporterKind = "jsonl"  // JSONL 文件
	ExporterFile   ExporterKind = "file"   // 独立 JSON 文件
	ExporterOTLP   ExporterKind = "otlp"   // OTLP 后端
)

// exporterFactory 导出器工厂接口。
type exporterFactory interface {
	kind() ExporterKind
	build(ctx context.Context, cfg Config, redactor *Redactor, dirManager *traceDirectoryManager) (sdktrace.SpanExporter, error)
}

// spanRecord Span 的导出数据结构。
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

// spanEventRecord Span 事件的导出数据结构。
type spanEventRecord struct {
	Name       string         `json:"name"`
	Time       time.Time      `json:"time"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// buildSpanExporter 根据类型创建对应的导出器。
func buildSpanExporter(ctx context.Context, cfg Config, kind ExporterKind, redactor *Redactor, dirManager *traceDirectoryManager) (sdktrace.SpanExporter, error) {
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
	return factory.build(ctx, cfg, redactor, dirManager)
}

// parseExporterKinds 解析逗号分隔的导出器类型字符串，返回去重后的列表。
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

// joinExporterKinds 将导出器类型列表合并为逗号分隔的字符串。
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

// spanRecordFromReadOnlySpan 将 OpenTelemetry Span 转换为可导出的 spanRecord。
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

// spanStatusFromReadOnlySpan 从 Span 属性中提取归一化的状态字符串。
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

// spanEventsToRecords 将 OpenTelemetry 事件列表转换为导出格式。
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

// attributeSetToMap 将 OpenTelemetry 属性集转换为 map，并对敏感信息进行脱敏。
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

// attributeStringValue 在属性集中查找指定键的值。
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

// attributeValueToAny 将 OpenTelemetry 属性值转换为 Go 任意类型。
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

// marshalSpanRecordJSON 将 spanRecord 序列化为便于阅读的 JSON。
func marshalSpanRecordJSON(record spanRecord) ([]byte, error) {
	payload, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(payload, '\n'), nil
}

// marshalSpanRecordJSONL 将 spanRecord 序列化为单行 JSON，适合 JSONL 追加写入。
func marshalSpanRecordJSONL(record spanRecord) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(record); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func sortedTraceRecords(records map[string]spanRecord) []spanRecord {
	if len(records) == 0 {
		return nil
	}
	out := make([]spanRecord, 0, len(records))
	for _, record := range records {
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartTime.Equal(out[j].StartTime) {
			return out[i].StartTime.Before(out[j].StartTime)
		}
		if !out[i].EndTime.Equal(out[j].EndTime) {
			return out[i].EndTime.Before(out[j].EndTime)
		}
		return out[i].SpanID < out[j].SpanID
	})
	return out
}

func writeTraceArtifacts(traceDir string, records []spanRecord) error {
	if strings.TrimSpace(traceDir) == "" || len(records) == 0 {
		return nil
	}
	summaryContent, err := buildTraceSummaryMarkdown(records)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(traceDir, "summary.md"), []byte(summaryContent), 0644); err != nil {
		return fmt.Errorf("write summary.md: %w", err)
	}

	timelineContent := buildTraceTimeline(records)
	if err := os.WriteFile(filepath.Join(traceDir, "timeline.txt"), []byte(timelineContent), 0644); err != nil {
		return fmt.Errorf("write timeline.txt: %w", err)
	}
	return nil
}

func buildTraceSummaryMarkdown(records []spanRecord) (string, error) {
	if len(records) == 0 {
		return "# Trace Summary\n\n暂无 Span 数据。\n", nil
	}

	first := records[0]
	startTime := first.StartTime
	endTime := first.EndTime
	errorCount := 0
	llmRequestCount := 0
	toolExecuteCount := 0
	scopes := make(map[string]struct{})
	toolNames := make(map[string]struct{})
	statusCount := make(map[string]int)
	rootSpanName := first.Name
	rootSpanDepth := estimateSpanDepth(first, records)

	for _, record := range records {
		if startTime.IsZero() || (!record.StartTime.IsZero() && record.StartTime.Before(startTime)) {
			startTime = record.StartTime
		}
		if endTime.IsZero() || (!record.EndTime.IsZero() && record.EndTime.After(endTime)) {
			endTime = record.EndTime
		}
		if record.Status != string(SpanStatusOK) {
			errorCount++
		}
		if record.Name == "llm.request" {
			llmRequestCount++
		}
		if record.Name == "tool.execute" {
			toolExecuteCount++
			if toolName, ok := stringMapValue(record.Attributes, "tool.name"); ok {
				toolNames[toolName] = struct{}{}
			}
		}
		if record.Scope != "" {
			scopes[record.Scope] = struct{}{}
		}
		statusCount[record.Status]++

		depth := estimateSpanDepth(record, records)
		if depth < rootSpanDepth {
			rootSpanDepth = depth
			rootSpanName = record.Name
		}
		if depth == rootSpanDepth && record.StartTime.Before(startTime) {
			rootSpanName = record.Name
		}
	}

	traceID := first.TraceID
	duration := endTime.Sub(startTime)
	serviceName, _ := stringMapValue(first.Resource, "service.name")
	environment, _ := stringMapValue(first.Resource, "deployment.environment")
	sessionID, _ := findFirstAttribute(records, "session.id")

	var builder strings.Builder
	builder.WriteString("# Trace Summary\n\n")
	builder.WriteString("## 基本信息\n")
	builder.WriteString(fmt.Sprintf("- Trace ID: `%s`\n", traceID))
	if sessionID != "" {
		builder.WriteString(fmt.Sprintf("- Session ID: `%s`\n", sessionID))
	}
	if rootSpanName != "" {
		builder.WriteString(fmt.Sprintf("- 根 Span: `%s`\n", rootSpanName))
	}
	if serviceName != "" {
		builder.WriteString(fmt.Sprintf("- 服务名: `%s`\n", serviceName))
	}
	if environment != "" {
		builder.WriteString(fmt.Sprintf("- 环境: `%s`\n", environment))
	}
	builder.WriteString(fmt.Sprintf("- 开始时间: `%s`\n", formatTimeForDisplay(startTime)))
	builder.WriteString(fmt.Sprintf("- 结束时间: `%s`\n", formatTimeForDisplay(endTime)))
	builder.WriteString(fmt.Sprintf("- 总耗时: `%s`\n", duration.String()))
	builder.WriteString("\n## 统计信息\n")
	builder.WriteString(fmt.Sprintf("- Span 总数: `%d`\n", len(records)))
	builder.WriteString(fmt.Sprintf("- 异常 Span 数: `%d`\n", errorCount))
	builder.WriteString(fmt.Sprintf("- LLM 请求数: `%d`\n", llmRequestCount))
	builder.WriteString(fmt.Sprintf("- Tool 执行数: `%d`\n", toolExecuteCount))
	if len(toolNames) > 0 {
		builder.WriteString(fmt.Sprintf("- Tool 列表: `%s`\n", strings.Join(sortedSetKeys(toolNames), "`, `")))
	}
	if len(scopes) > 0 {
		builder.WriteString(fmt.Sprintf("- Scope 列表: `%s`\n", strings.Join(sortedSetKeys(scopes), "`, `")))
	}
	if len(statusCount) > 0 {
		builder.WriteString("\n## 状态分布\n")
		for _, status := range sortedCountKeys(statusCount) {
			builder.WriteString(fmt.Sprintf("- `%s`: `%d`\n", status, statusCount[status]))
		}
	}
	builder.WriteString("\n## 文件说明\n")
	builder.WriteString("- `spans.jsonl`: 原始 Span 明细，一行一个 Span。\n")
	builder.WriteString("- `timeline.txt`: 按时间排序的可读时间线。\n")
	return builder.String(), nil
}

func buildTraceTimeline(records []spanRecord) string {
	if len(records) == 0 {
		return "暂无 Span 数据。\n"
	}

	startTime := records[0].StartTime
	indexBySpanID := make(map[string]spanRecord, len(records))
	for _, record := range records {
		indexBySpanID[record.SpanID] = record
		if record.StartTime.Before(startTime) {
			startTime = record.StartTime
		}
	}

	var builder strings.Builder
	builder.WriteString("Trace Timeline\n")
	builder.WriteString("==============\n")
	for idx, record := range records {
		offset := record.StartTime.Sub(startTime)
		duration := record.EndTime.Sub(record.StartTime)
		depth := estimateSpanDepthFromIndex(record, indexBySpanID)
		indent := strings.Repeat("  ", depth)
		builder.WriteString(fmt.Sprintf(
			"%02d. %s +%s [%s] %s%s (%s, %s)\n",
			idx+1,
			formatTimeForDisplay(record.StartTime),
			formatDurationMS(offset),
			record.Status,
			indent,
			record.Name,
			record.Scope,
			formatDurationMS(duration),
		))
		if description := strings.TrimSpace(record.StatusDescription); description != "" {
			builder.WriteString(fmt.Sprintf("    %sstatus_description: %s\n", indent, description))
		}
		if attrLine := buildTimelineAttributeLine(record); attrLine != "" {
			builder.WriteString(fmt.Sprintf("    %sattrs: %s\n", indent, attrLine))
		}
	}
	return builder.String()
}

func buildTimelineAttributeLine(record spanRecord) string {
	keys := []string{
		"session.id",
		"tool.name",
		"tool.call_id",
		"tool.summary",
		"agent.model",
		"agent.provider",
		"llm.provider",
		"llm.model",
		"llm.usage.input_tokens",
		"llm.usage.output_tokens",
		"action.id",
		"error.category",
	}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value, ok := record.Attributes[key]
		if !ok {
			continue
		}
		text := strings.TrimSpace(stringifyValue(value))
		if text == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", key, text))
	}
	return strings.Join(parts, ", ")
}

func estimateSpanDepth(record spanRecord, records []spanRecord) int {
	index := make(map[string]spanRecord, len(records))
	for _, item := range records {
		index[item.SpanID] = item
	}
	return estimateSpanDepthFromIndex(record, index)
}

func estimateSpanDepthFromIndex(record spanRecord, index map[string]spanRecord) int {
	depth := 0
	parentID := strings.TrimSpace(record.ParentSpanID)
	for parentID != "" {
		parent, ok := index[parentID]
		if !ok {
			break
		}
		depth++
		parentID = strings.TrimSpace(parent.ParentSpanID)
	}
	return depth
}

func findFirstAttribute(records []spanRecord, key string) (string, bool) {
	for _, record := range records {
		if value, ok := stringMapValue(record.Attributes, key); ok {
			return value, true
		}
	}
	return "", false
}

func stringMapValue(values map[string]any, key string) (string, bool) {
	if len(values) == 0 {
		return "", false
	}
	value, ok := values[key]
	if !ok {
		return "", false
	}
	text := strings.TrimSpace(stringifyValue(value))
	if text == "" {
		return "", false
	}
	return text, true
}

func sortedSetKeys(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sortedCountKeys(values map[string]int) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func formatTimeForDisplay(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.Format("2006-01-02 15:04:05.000")
}

func formatDurationMS(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%.3fms", float64(d)/float64(time.Millisecond))
}
