package observability

import (
	"context"
	"strings"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/schema"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const einoTracerName = "github.com/wen/opentalon/internal/observability/eino"

// NewEinoTracingHandler 返回统一的 Eino 链路追踪回调处理器，
// 在框架边界观测组件、Graph、模型和工具的执行过程。
//
// 可以在进程启动时通过 callbacks.AppendGlobalHandlers 注册一次，
// 也可以通过 compose.WithCallbacks 只注入单次调用。
// 业务节点不需要自行创建或结束 Span。
func NewEinoTracingHandler() callbacks.Handler {
	return callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
			return startEinoSpan(ctx, info, false)
		}).
		OnEndFn(func(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackOutput) context.Context {
			finishEinoSpan(ctx, nil)
			return ctx
		}).
		OnErrorFn(func(ctx context.Context, _ *callbacks.RunInfo, err error) context.Context {
			finishEinoSpan(ctx, err)
			return ctx
		}).
		OnStartWithStreamInputFn(func(ctx context.Context, info *callbacks.RunInfo, input *schema.StreamReader[callbacks.CallbackInput]) context.Context {
			if input != nil {
				input.Close()
			}
			return startEinoSpan(ctx, info, true)
		}).
		OnEndWithStreamOutputFn(func(ctx context.Context, _ *callbacks.RunInfo, output *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
			if output != nil {
				output.Close()
			}
			finishEinoSpan(ctx, nil)
			return ctx
		}).
		Build()
}

func startEinoSpan(ctx context.Context, info *callbacks.RunInfo, streaming bool) context.Context {
	component, implementation, name := einoRunInfo(info)
	attributes := []attribute.KeyValue{
		attribute.String("eino.component", component),
		attribute.Bool("eino.streaming", streaming),
	}
	if implementation != "" {
		attributes = append(attributes, attribute.String("eino.type", implementation))
	}
	if name != "" {
		attributes = append(attributes, attribute.String("eino.name", name))
	}
	ctx, _ = otel.Tracer(einoTracerName).Start(ctx, einoSpanName(component, name, implementation),
		oteltrace.WithAttributes(attributes...))
	return ctx
}

func finishEinoSpan(ctx context.Context, err error) {
	span := oteltrace.SpanFromContext(ctx)
	defer span.End()
	if err != nil {
		recordSpanError(span, err)
		return
	}
	markSpanOK(span)
}

func einoRunInfo(info *callbacks.RunInfo) (component, implementation, name string) {
	if info == nil {
		return "component", "", ""
	}
	component = strings.TrimSpace(string(info.Component))
	if component == "" {
		component = "component"
	}
	return component, strings.TrimSpace(info.Type), strings.TrimSpace(info.Name)
}

func einoSpanName(component, name, implementation string) string {
	detail := name
	if detail == "" {
		detail = implementation
	}
	if detail == "" {
		return "eino." + component
	}
	return "eino." + component + "." + detail
}
