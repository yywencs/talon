package observability

import (
	"context"
	"errors"
	"testing"

	"github.com/wen/opentalon/internal/platform"
	"go.opentelemetry.io/otel"
	otelattribute "go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type observedPlatformStub struct {
	platform.ToolOpsPlatform
	routes  []platform.Route
	err     error
	traceID string
}

func (s *observedPlatformStub) GetRoutes(ctx context.Context, _ platform.StateQuery) ([]platform.Route, error) {
	s.traceID = TraceIDFromContext(ctx)
	return s.routes, s.err
}

func TestObservePlatformCreatesBoundarySpan(t *testing.T) {
	stub := &observedPlatformStub{routes: []platform.Route{{ID: "route-a"}}}
	service := ObservePlatform(stub)
	// 有意在 Provider 之前组装装饰器，验证启动时的依赖组装顺序
	// 不会导致装饰器永久持有无操作 Tracer。
	recorder := installSpanRecorder(t)

	routes, err := service.GetRoutes(context.Background(), platform.StateQuery{
		Scope: platform.Scope{IncidentID: "incident-001"},
	})
	if err != nil {
		t.Fatalf("GetRoutes() error = %v", err)
	}
	if len(routes) != 1 || routes[0].ID != "route-a" {
		t.Fatalf("GetRoutes() = %#v, want route-a", routes)
	}
	if stub.traceID == "" {
		t.Fatal("wrapped platform did not receive trace context")
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	if spans[0].Name() != "toolops.platform.get_routes" {
		t.Fatalf("span name = %q", spans[0].Name())
	}
	attributes := spanAttributeMap(spans[0].Attributes())
	if attributes["toolops.incident.id"] != "incident-001" {
		t.Fatalf("incident attribute = %#v", attributes["toolops.incident.id"])
	}
	if attributes[AttrSpanStatus] != string(SpanStatusOK) {
		t.Fatalf("status attribute = %#v", attributes[AttrSpanStatus])
	}
}

func TestObservePlatformRecordsErrorsWithoutChangingResult(t *testing.T) {
	recorder := installSpanRecorder(t)
	wantErr := errors.New("platform unavailable")
	service := ObservePlatform(&observedPlatformStub{err: wantErr})

	_, err := service.GetRoutes(context.Background(), platform.StateQuery{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("GetRoutes() error = %v, want %v", err, wantErr)
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	attributes := spanAttributeMap(spans[0].Attributes())
	if attributes[AttrSpanStatus] != string(SpanStatusError) {
		t.Fatalf("status attribute = %#v", attributes[AttrSpanStatus])
	}
	if len(spans[0].Events()) != 1 || spans[0].Events()[0].Name != "exception" {
		t.Fatalf("events = %#v, want one exception", spans[0].Events())
	}
}

func TestObservePlatformPreservesNil(t *testing.T) {
	if ObservePlatform(nil) != nil {
		t.Fatal("ObservePlatform(nil) must return nil")
	}
}

func TestObservePlatformDoesNotDoubleWrap(t *testing.T) {
	first := ObservePlatform(&observedPlatformStub{})
	if second := ObservePlatform(first); second != first {
		t.Fatal("ObservePlatform wrapped an already observed platform")
	}
}

func installSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	globalProvider.mu.Lock()
	globalProvider.tp = provider
	globalProvider.mu.Unlock()
	otel.SetTracerProvider(provider)

	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		globalProvider.mu.Lock()
		if globalProvider.tp == provider {
			globalProvider.tp = nil
		}
		globalProvider.mu.Unlock()
		otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample())))
	})
	return recorder
}

func spanAttributeMap(attributes []otelattribute.KeyValue) map[string]any {
	result := make(map[string]any, len(attributes))
	for _, attribute := range attributes {
		result[string(attribute.Key)] = attribute.Value.AsInterface()
	}
	return result
}
