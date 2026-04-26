package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type payloadArtifactRecord struct {
	TraceID    string    `json:"trace_id"`
	SpanID     string    `json:"span_id"`
	Kind       string    `json:"kind"`
	CapturedAt time.Time `json:"captured_at"`
	Payload    any       `json:"payload"`
}

// PayloadArtifactPath 返回当前上下文对应 payload artifact 的落盘路径。
func PayloadArtifactPath(ctx context.Context, kind string) (string, error) {
	kind, err := normalizePayloadArtifactKind(kind)
	if err != nil {
		return "", err
	}
	traceID, spanID, dirManager, err := payloadArtifactContext(ctx)
	if err != nil {
		return "", err
	}
	traceDir, err := dirManager.TraceDirForTraceID(traceID, time.Now())
	if err != nil {
		return "", fmt.Errorf("resolve trace dir: %w", err)
	}
	return filepath.Join(traceDir, buildPayloadArtifactFileName(spanID, kind, time.Now())), nil
}

// WritePayloadArtifact 将 payload 写入与当前 trace 绑定的 JSON artifact 文件。
func WritePayloadArtifact(ctx context.Context, kind string, payload any) (string, error) {
	kind, err := normalizePayloadArtifactKind(kind)
	if err != nil {
		return "", err
	}
	traceID, spanID, dirManager, err := payloadArtifactContext(ctx)
	if err != nil {
		return "", err
	}
	normalizedPayload, err := normalizePayloadArtifactValue(payload)
	if err != nil {
		return "", fmt.Errorf("normalize payload artifact: %w", err)
	}
	redactedPayload, err := redactPayloadValue(normalizedPayload)
	if err != nil {
		return "", err
	}
	capturedAt := time.Now()
	traceDir, err := dirManager.TraceDirForTraceID(traceID, capturedAt)
	if err != nil {
		return "", fmt.Errorf("resolve trace dir: %w", err)
	}
	filePath := filepath.Join(traceDir, buildPayloadArtifactFileName(spanID, kind, capturedAt))
	record := payloadArtifactRecord{
		TraceID:    traceID,
		SpanID:     spanID,
		Kind:       kind,
		CapturedAt: capturedAt,
		Payload:    redactedPayload,
	}
	fileContent, err := marshalPayloadArtifactJSON(record)
	if err != nil {
		return "", fmt.Errorf("marshal payload artifact: %w", err)
	}
	if err := os.WriteFile(filePath, fileContent, 0644); err != nil {
		return "", fmt.Errorf("write payload artifact: %w", err)
	}
	return filePath, nil
}

func payloadArtifactContext(ctx context.Context) (string, string, *traceDirectoryManager, error) {
	traceID := strings.TrimSpace(TraceIDFromContext(ctx))
	if traceID == "" {
		return "", "", nil, fmt.Errorf("trace_id is empty in context")
	}
	spanID := strings.TrimSpace(SpanIDFromContext(ctx))
	if spanID == "" {
		return "", "", nil, fmt.Errorf("span_id is empty in context")
	}
	dirManager := currentTraceDirectoryManager()
	if dirManager == nil {
		return "", "", nil, fmt.Errorf("trace directory manager is not initialized")
	}
	return traceID, spanID, dirManager, nil
}

func normalizePayloadArtifactValue(payload any) (any, error) {
	switch value := payload.(type) {
	case nil:
		return nil, nil
	case string:
		return decodeJSONTextOrString(value)
	case []byte:
		return decodeJSONTextOrString(string(value))
	case json.RawMessage:
		return decodeJSONTextOrString(string(value))
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("payload cannot be serialized: %w", err)
		}
		return decodeJSONTextOrString(string(raw))
	}
}

func decodeJSONTextOrString(raw string) (any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return raw, nil
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return raw, nil
	} else if err != io.EOF {
		return raw, nil
	}
	return payload, nil
}

func normalizePayloadArtifactKind(kind string) (string, error) {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return "", fmt.Errorf("payload artifact kind is empty")
	}
	return kind, nil
}

func marshalPayloadArtifactJSON(record payloadArtifactRecord) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(record); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func buildPayloadArtifactFileName(spanID, kind string, capturedAt time.Time) string {
	if capturedAt.IsZero() {
		capturedAt = time.Now()
	}
	parts := []string{
		formatTimestampForPath(capturedAt),
		strings.TrimSpace(spanID),
		sanitizePathToken(kind),
		"payload",
	}
	return strings.Join(parts, "-") + ".json"
}

func redactPayloadValue(payload any) (any, error) {
	redactor := currentRedactor()
	if redactor == nil {
		return nil, fmt.Errorf("redactor is not initialized")
	}
	return redactor.RedactValue("", payload), nil
}
