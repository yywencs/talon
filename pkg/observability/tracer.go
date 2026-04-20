package observability

import (
	"context"

	"go.opentelemetry.io/otel"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const defaultTracerName = "github.com/wen/opentalon/pkg/observability"

// SpanKind 表示 Span 的语义类型。
type SpanKind string

const (
	// SpanKindInternal 表示内部处理流程。
	SpanKindInternal SpanKind = "internal"
	// SpanKindClient 表示客户端请求。
	SpanKindClient SpanKind = "client"
	// SpanKindServer 表示服务端入口。
	SpanKindServer SpanKind = "server"
	// SpanKindProducer 表示消息生产者。
	SpanKindProducer SpanKind = "producer"
	// SpanKindConsumer 表示消息消费者。
	SpanKindConsumer SpanKind = "consumer"
)

// Tracer 定义业务层可使用的 tracer 抽象。
type Tracer interface {
	StartSpan(ctx context.Context, name string, opts ...SpanOption) (context.Context, Span)
}

type tracerWrapper struct {
	tracer oteltrace.Tracer
}

type noopTracer struct{}

type startSpanConfig struct {
	kind  SpanKind
	attrs []Attribute
}

// SpanOption 表示 StartSpan 的可选参数。
type SpanOption interface {
	apply(*startSpanConfig)
}

type spanOptionFunc func(*startSpanConfig)

func (f spanOptionFunc) apply(cfg *startSpanConfig) {
	f(cfg)
}

// WithSpanKind 设置 SpanKind。
func WithSpanKind(kind SpanKind) SpanOption {
	return spanOptionFunc(func(cfg *startSpanConfig) {
		cfg.kind = kind
	})
}

// WithAttributes 设置初始 attributes。
func WithAttributes(attrs ...Attribute) SpanOption {
	return spanOptionFunc(func(cfg *startSpanConfig) {
		cfg.attrs = append(cfg.attrs, attrs...)
	})
}

// GlobalTracer 返回具名 tracer。
func GlobalTracer(name string) Tracer {
	if name == "" {
		name = defaultTracerName
	}
	if currentTracerProvider() == nil {
		return noopTracer{}
	}
	return tracerWrapper{tracer: otel.Tracer(name)}
}

// TracerFor 返回具名 tracer。
func TracerFor(name string) Tracer {
	return GlobalTracer(name)
}

// StartSpan 使用默认 tracer 创建 Span。
func StartSpan(ctx context.Context, name string, opts ...SpanOption) (context.Context, Span) {
	return GlobalTracer(defaultTracerName).StartSpan(ctx, name, opts...)
}

func (t tracerWrapper) StartSpan(ctx context.Context, name string, opts ...SpanOption) (context.Context, Span) {
	cfg := startSpanConfig{kind: SpanKindInternal}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		opt.apply(&cfg)
	}
	spanOptions := []oteltrace.SpanStartOption{oteltrace.WithSpanKind(toOTelSpanKind(cfg.kind))}
	if len(cfg.attrs) > 0 {
		spanOptions = append(spanOptions, oteltrace.WithAttributes(toOTelAttributes(cfg.attrs)...))
	}
	ctx, span := t.tracer.Start(ctx, name, spanOptions...)
	return ctx, newSpanWrapper(span)
}

func (noopTracer) StartSpan(ctx context.Context, name string, opts ...SpanOption) (context.Context, Span) {
	_ = name
	_ = opts
	return ctx, newNoopSpan()
}

func toOTelSpanKind(kind SpanKind) oteltrace.SpanKind {
	switch kind {
	case SpanKindClient:
		return oteltrace.SpanKindClient
	case SpanKindServer:
		return oteltrace.SpanKindServer
	case SpanKindProducer:
		return oteltrace.SpanKindProducer
	case SpanKindConsumer:
		return oteltrace.SpanKindConsumer
	default:
		return oteltrace.SpanKindInternal
	}
}
