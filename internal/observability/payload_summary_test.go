package observability

import (
	"context"
	"strings"
	"testing"
)

func TestSummarizePayloadAppliesRedactionPreviewAndHash(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Exporters = []ExporterKind{ExporterJSONL}
	cfg.TraceDir = t.TempDir()
	cfg.PayloadSizeLimit = 4096
	cfg.PayloadPreviewLimit = 4096

	if err := Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() {
		_ = Shutdown(context.Background())
	})

	payload := map[string]any{
		"authorization": "Bearer secret-token",
		"message":       "hello",
	}

	summary, err := SummarizePayload(payload)
	if err != nil {
		t.Fatalf("SummarizePayload() error = %v", err)
	}

	if summary.Truncated {
		t.Fatalf("Truncated = true, want false")
	}
	if summary.RawSizeBytes <= 0 {
		t.Fatalf("RawSizeBytes = %d, want > 0", summary.RawSizeBytes)
	}
	if strings.Contains(summary.Preview, "secret-token") {
		t.Fatalf("Preview leaked secret token: %q", summary.Preview)
	}
	if summary.SHA256 == "" {
		t.Fatal("SHA256 is empty")
	}

	redactedPayload, ok := summary.Payload.(map[string]any)
	if !ok {
		t.Fatalf("Payload type = %T, want map[string]any", summary.Payload)
	}
	if got := redactedPayload["authorization"]; got != defaultRedactionReplacement {
		t.Fatalf("authorization = %v, want %q", got, defaultRedactionReplacement)
	}
	if got := payload["authorization"]; got != "Bearer secret-token" {
		t.Fatalf("input payload mutated, authorization = %v", got)
	}

	otherSummary, err := SummarizePayload(map[string]any{
		"authorization": "Bearer another-token",
		"message":       "hello",
	})
	if err != nil {
		t.Fatalf("SummarizePayload() second call error = %v", err)
	}
	if otherSummary.SHA256 != summary.SHA256 {
		t.Fatalf("SHA256 mismatch for same redacted payload: got %q want %q", otherSummary.SHA256, summary.SHA256)
	}
}

func TestSummarizePayloadTruncatesLargePayload(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Exporters = []ExporterKind{ExporterJSONL}
	cfg.TraceDir = t.TempDir()
	cfg.PayloadSizeLimit = 24
	cfg.PayloadPreviewLimit = 12

	if err := Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() {
		_ = Shutdown(context.Background())
	})

	summary, err := SummarizePayload(strings.Repeat("abcdef", 10))
	if err != nil {
		t.Fatalf("SummarizePayload() error = %v", err)
	}

	if !summary.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	if summary.RawSizeBytes != 60 {
		t.Fatalf("RawSizeBytes = %d, want 60", summary.RawSizeBytes)
	}
	if !strings.HasSuffix(summary.Preview, payloadPreviewTruncatedSuffix) {
		t.Fatalf("Preview = %q, want truncated suffix", summary.Preview)
	}
	payloadText, ok := summary.Payload.(string)
	if !ok {
		t.Fatalf("Payload type = %T, want string", summary.Payload)
	}
	if !strings.HasSuffix(payloadText, payloadPreviewTruncatedSuffix) {
		t.Fatalf("Payload = %q, want truncated suffix", payloadText)
	}
}

func TestSummarizePayloadHandlesNilAndPlainString(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Exporters = []ExporterKind{ExporterJSONL}
	cfg.TraceDir = t.TempDir()

	if err := Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() {
		_ = Shutdown(context.Background())
	})

	nilSummary, err := SummarizePayload(nil)
	if err != nil {
		t.Fatalf("SummarizePayload(nil) error = %v", err)
	}
	if nilSummary.RawSizeBytes != 0 {
		t.Fatalf("RawSizeBytes = %d, want 0", nilSummary.RawSizeBytes)
	}
	if nilSummary.Preview != "" {
		t.Fatalf("Preview = %q, want empty", nilSummary.Preview)
	}
	if nilSummary.SHA256 == "" {
		t.Fatal("SHA256 is empty for nil payload")
	}

	textSummary, err := SummarizePayload("plain-text")
	if err != nil {
		t.Fatalf("SummarizePayload(plain-text) error = %v", err)
	}
	if got, ok := textSummary.Payload.(string); !ok || got != "plain-text" {
		t.Fatalf("Payload = %#v, want plain-text", textSummary.Payload)
	}
	if textSummary.RawSizeBytes != len("plain-text") {
		t.Fatalf("RawSizeBytes = %d, want %d", textSummary.RawSizeBytes, len("plain-text"))
	}
}

func TestSummarizePayloadReturnsStableErrors(t *testing.T) {
	if err := Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	if _, err := SummarizePayload(map[string]any{"message": "hello"}); err == nil || !strings.Contains(err.Error(), "redactor is not initialized") {
		t.Fatalf("error = %v, want redactor is not initialized", err)
	}

	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Exporters = []ExporterKind{ExporterJSONL}
	cfg.TraceDir = t.TempDir()
	if err := Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() {
		_ = Shutdown(context.Background())
	})

	if _, err := SummarizePayload(make(chan int)); err == nil || !strings.Contains(err.Error(), "payload cannot be serialized") {
		t.Fatalf("error = %v, want payload cannot be serialized", err)
	}
}
