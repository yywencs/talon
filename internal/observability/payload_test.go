package observability

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWritePayloadArtifactSharesTraceDirectoryWithTimeline(t *testing.T) {
	traceRoot := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Exporters = []ExporterKind{ExporterJSONL}
	cfg.TraceDir = traceRoot

	if err := Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() {
		_ = Shutdown(context.Background())
	})

	ctx, span := StartSpan(context.Background(), "payload.test")
	traceID := TraceIDFromContext(ctx)
	spanID := SpanIDFromContext(ctx)

	payloadPath, err := WritePayloadArtifact(ctx, "request", map[string]any{
		"authorization": "Bearer secret-token",
		"nested": map[string]any{
			"api_key": "secret-key",
		},
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "hello",
			},
		},
	})
	if err != nil {
		t.Fatalf("WritePayloadArtifact() error = %v", err)
	}

	span.End()
	if err := Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	traceDir := filepath.Dir(payloadPath)
	if _, err := os.Stat(filepath.Join(traceDir, "timeline.txt")); err != nil {
		t.Fatalf("timeline.txt not found in trace dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(traceDir, "spans.jsonl")); err != nil {
		t.Fatalf("spans.jsonl not found in trace dir: %v", err)
	}

	raw, err := os.ReadFile(payloadPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var record struct {
		TraceID string         `json:"trace_id"`
		SpanID  string         `json:"span_id"`
		Kind    string         `json:"kind"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if record.TraceID != traceID {
		t.Fatalf("trace_id = %q, want %q", record.TraceID, traceID)
	}
	if record.SpanID != spanID {
		t.Fatalf("span_id = %q, want %q", record.SpanID, spanID)
	}
	if record.Kind != "request" {
		t.Fatalf("kind = %q, want request", record.Kind)
	}
	if got := record.Payload["authorization"]; got != defaultRedactionReplacement {
		t.Fatalf("authorization = %v, want %q", got, defaultRedactionReplacement)
	}
	nested, ok := record.Payload["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested payload type = %T, want map[string]any", record.Payload["nested"])
	}
	if got := nested["api_key"]; got != defaultRedactionReplacement {
		t.Fatalf("nested api_key = %v, want %q", got, defaultRedactionReplacement)
	}
}

func TestWritePayloadArtifactAcceptsRawJSONString(t *testing.T) {
	traceRoot := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Exporters = []ExporterKind{ExporterJSONL}
	cfg.TraceDir = traceRoot

	if err := Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() {
		_ = Shutdown(context.Background())
	})

	ctx, span := StartSpan(context.Background(), "payload.raw")
	payloadPath, err := WritePayloadArtifact(ctx, "response", `{"message":"ok","authorization":"Bearer secret-token"}`)
	if err != nil {
		t.Fatalf("WritePayloadArtifact() error = %v", err)
	}
	span.End()

	raw, err := os.ReadFile(payloadPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var record struct {
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got := record.Payload["message"]; got != "ok" {
		t.Fatalf("message = %v, want ok", got)
	}
	if got := record.Payload["authorization"]; got != defaultRedactionReplacement {
		t.Fatalf("authorization = %v, want %q", got, defaultRedactionReplacement)
	}
}
