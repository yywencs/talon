package observability

import (
	"context"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/compose"
)

// BeginCallback 使用已经配置的 Eino CozeLoop Handler 开始一个可嵌套阶段。
// 返回的 finish 必须调用一次；未启用 CozeLoop 时它是无操作函数。
func BeginCallback(ctx context.Context, name string, input any) (context.Context, func(any, error)) {
	handler := EinoHandler()
	if handler == nil {
		return ctx, func(any, error) {}
	}
	info := &callbacks.RunInfo{Name: name, Type: "ToolOps", Component: compose.ComponentOfLambda}
	ctx = handler.OnStart(ctx, info, input)
	return ctx, func(output any, err error) {
		if err != nil {
			handler.OnError(ctx, info, err)
			return
		}
		handler.OnEnd(ctx, info, output)
	}
}

// RunCallback 执行一个有返回值的阶段，并通过现有 CozeLoop Callback 上报输入、输出和错误。
func RunCallback[T any](ctx context.Context, name string, input any, run func(context.Context) (T, error)) (T, error) {
	ctx, finish := BeginCallback(ctx, name, input)
	output, err := run(ctx)
	finish(output, err)
	return output, err
}
