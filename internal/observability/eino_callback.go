package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	loopcallback "github.com/cloudwego/eino-ext/callbacks/cozeloop"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/schema"
)

type safeDataParser struct {
	next     loopcallback.CallbackDataParser
	redactor *Redactor
}

func newSafeDataParser(next loopcallback.CallbackDataParser, redactor *Redactor) loopcallback.CallbackDataParser {
	return &safeDataParser{next: next, redactor: redactor}
}

func (p *safeDataParser) ParseInput(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) map[string]any {
	return p.sanitize(p.next.ParseInput(ctx, info, input))
}

func (p *safeDataParser) ParseOutput(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) map[string]any {
	return p.sanitize(p.next.ParseOutput(ctx, info, output))
}

func (p *safeDataParser) ParseStreamInput(ctx context.Context, info *callbacks.RunInfo, input *schema.StreamReader[callbacks.CallbackInput]) map[string]any {
	return p.sanitize(p.next.ParseStreamInput(ctx, info, input))
}

func (p *safeDataParser) ParseStreamOutput(ctx context.Context, info *callbacks.RunInfo, output *schema.StreamReader[callbacks.CallbackOutput]) map[string]any {
	return p.sanitize(p.next.ParseStreamOutput(ctx, info, output))
}

func (p *safeDataParser) sanitize(tags map[string]any) map[string]any {
	if len(tags) == 0 {
		return tags
	}
	safe := make(map[string]any, len(tags))
	for key, value := range tags {
		safe[key] = safeTraceValue(p.redactor, key, value)
	}
	return safe
}

// safeTraceValue 先把 Eino 的结构体转换为普通 JSON 数据，再递归脱敏。
// 无法安全序列化或未初始化脱敏器时采用关闭式策略。
func safeTraceValue(redactor *Redactor, path string, value any) any {
	if redactor == nil {
		return defaultRedactionReplacement
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return defaultRedactionReplacement
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return defaultRedactionReplacement
	}
	normalized = normalizeTraceNumbers(normalized)
	return redactor.RedactValue(path, normalized)
}

// normalizeTraceNumbers 将 JSON 解码产生的 json.Number 还原为 CozeLoop
// 能识别的原生数字类型。整数优先使用 int64，小数使用 float64。
func normalizeTraceNumbers(value any) any {
	switch v := value.(type) {
	case json.Number:
		if integer, err := v.Int64(); err == nil {
			return integer
		}
		if decimal, err := v.Float64(); err == nil {
			return decimal
		}
		return defaultRedactionReplacement
	case map[string]any:
		for key, item := range v {
			v[key] = normalizeTraceNumbers(item)
		}
		return v
	case []any:
		for index, item := range v {
			v[index] = normalizeTraceNumbers(item)
		}
		return v
	default:
		return value
	}
}

type safeEinoHandler struct {
	next     callbacks.Handler
	redactor *Redactor
}

func newSafeEinoHandler(next callbacks.Handler, redactor *Redactor) callbacks.Handler {
	return &safeEinoHandler{next: next, redactor: redactor}
}

func (h *safeEinoHandler) OnStart(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
	return h.next.OnStart(ctx, info, input)
}

func (h *safeEinoHandler) OnEnd(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
	return h.next.OnEnd(ctx, info, output)
}

func (h *safeEinoHandler) OnError(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
	return h.next.OnError(ctx, info, safeTraceError(h.redactor, err))
}

func (h *safeEinoHandler) OnStartWithStreamInput(ctx context.Context, info *callbacks.RunInfo, input *schema.StreamReader[callbacks.CallbackInput]) context.Context {
	return h.next.OnStartWithStreamInput(ctx, info, input)
}

func (h *safeEinoHandler) OnEndWithStreamOutput(ctx context.Context, info *callbacks.RunInfo, output *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
	return h.next.OnEndWithStreamOutput(ctx, info, output)
}

func safeTraceError(redactor *Redactor, err error) error {
	if err == nil {
		return nil
	}
	value := safeTraceValue(redactor, "error", err.Error())
	message, ok := value.(string)
	if !ok || message == "" {
		message = defaultRedactionReplacement
	}
	return errors.New(message)
}
