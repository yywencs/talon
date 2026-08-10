package observability

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
)

func TestEinoTracingHandlerCreatesComponentSpan(t *testing.T) {
	recorder := installSpanRecorder(t)
	handler := NewEinoTracingHandler()
	info := &callbacks.RunInfo{
		Name:      "ToolOpsModel",
		Type:      "OpenAI",
		Component: components.ComponentOfChatModel,
	}

	ctx := handler.OnStart(context.Background(), info, nil)
	if TraceIDFromContext(ctx) == "" {
		t.Fatal("Eino callback did not return a trace context")
	}
	handler.OnEnd(ctx, info, nil)

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	if spans[0].Name() != "eino.ChatModel.ToolOpsModel" {
		t.Fatalf("span name = %q", spans[0].Name())
	}
	attributes := spanAttributeMap(spans[0].Attributes())
	if attributes["eino.component"] != "ChatModel" || attributes["eino.type"] != "OpenAI" {
		t.Fatalf("Eino attributes = %#v", attributes)
	}
	if attributes[AttrSpanStatus] != string(SpanStatusOK) {
		t.Fatalf("status attribute = %#v", attributes[AttrSpanStatus])
	}
}

func TestEinoTracingHandlerRecordsComponentError(t *testing.T) {
	recorder := installSpanRecorder(t)
	handler := NewEinoTracingHandler()
	info := &callbacks.RunInfo{Name: "query_metrics", Component: components.ComponentOfTool}
	wantErr := errors.New("tool failed")

	ctx := handler.OnStart(context.Background(), info, nil)
	handler.OnError(ctx, info, wantErr)

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	if spans[0].Name() != "eino.Tool.query_metrics" {
		t.Fatalf("span name = %q", spans[0].Name())
	}
	attributes := spanAttributeMap(spans[0].Attributes())
	if attributes[AttrSpanStatus] != string(SpanStatusError) {
		t.Fatalf("status attribute = %#v", attributes[AttrSpanStatus])
	}
}
