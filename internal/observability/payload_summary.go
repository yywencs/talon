package observability

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const payloadPreviewTruncatedSuffix = "...[TRUNCATED]"

// PayloadSummary 表示 payload 的标准化摘要结果。
type PayloadSummary struct {
	Payload      any
	RawSizeBytes int
	Preview      string
	SHA256       string
	Truncated    bool
}

// SummarizePayload 生成 payload 的大小、预览、哈希和截断摘要。
func SummarizePayload(payload any) (PayloadSummary, error) {
	rawInputBytes, err := rawPayloadBytes(payload)
	if err != nil {
		return PayloadSummary{}, fmt.Errorf("serialize payload: %w", err)
	}
	normalizedPayload, err := normalizePayloadArtifactValue(payload)
	if err != nil {
		return PayloadSummary{}, fmt.Errorf("normalize payload: %w", err)
	}

	redactedPayload, err := redactPayloadValue(normalizedPayload)
	if err != nil {
		return PayloadSummary{}, err
	}

	redactedBytes, err := encodedPayloadBytes(redactedPayload)
	if err != nil {
		return PayloadSummary{}, fmt.Errorf("serialize redacted payload: %w", err)
	}

	cfg := currentConfig().Normalize()
	truncatedPayloadText, truncated := truncatePayloadText(string(redactedBytes), cfg.PayloadSizeLimit)
	preview := buildPayloadPreview(string(redactedBytes), cfg.PayloadPreviewLimit)

	return PayloadSummary{
		Payload:      payloadSummaryValue(redactedPayload, truncatedPayloadText, truncated),
		RawSizeBytes: len(rawInputBytes),
		Preview:      preview,
		SHA256:       payloadSHA256(redactedBytes),
		Truncated:    truncated,
	}, nil
}

func rawPayloadBytes(payload any) ([]byte, error) {
	switch value := payload.(type) {
	case nil:
		return []byte{}, nil
	case string:
		return []byte(value), nil
	case []byte:
		return append([]byte(nil), value...), nil
	case json.RawMessage:
		return append([]byte(nil), value...), nil
	default:
		raw, err := marshalPayloadValue(value)
		if err != nil {
			return nil, fmt.Errorf("payload cannot be serialized: %w", err)
		}
		return raw, nil
	}
}

func encodedPayloadBytes(payload any) ([]byte, error) {
	switch value := payload.(type) {
	case nil:
		return []byte{}, nil
	case string:
		return []byte(value), nil
	default:
		return marshalPayloadValue(value)
	}
}

func marshalPayloadValue(payload any) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func truncatePayloadText(payload string, limit int) (string, bool) {
	if limit <= 0 {
		return payload, false
	}
	if len(payload) <= limit {
		return payload, false
	}
	return payload[:limit] + payloadPreviewTruncatedSuffix, true
}

func buildPayloadPreview(payload string, limit int) string {
	if limit <= 0 || payload == "" {
		return payload
	}
	if len(payload) <= limit {
		return payload
	}
	return payload[:limit] + payloadPreviewTruncatedSuffix
}

func payloadSummaryValue(redactedPayload any, truncatedText string, truncated bool) any {
	if !truncated {
		return redactedPayload
	}
	return strings.TrimSpace(truncatedText)
}

func payloadSHA256(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// PayloadSummaryAttributes 将 payload 摘要转换为一组稳定的 Span attributes。
func PayloadSummaryAttributes(prefix string, summary PayloadSummary, artifactPath string) []Attribute {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil
	}
	attrs := []Attribute{
		Int(prefix+".body_size", summary.RawSizeBytes),
		String(prefix+".preview", summary.Preview),
		String(prefix+".sha256", summary.SHA256),
		Bool(prefix+".truncated", summary.Truncated),
	}
	if strings.TrimSpace(artifactPath) != "" {
		attrs = append(attrs, String(prefix+".artifact_path", artifactPath))
	}
	return attrs
}
