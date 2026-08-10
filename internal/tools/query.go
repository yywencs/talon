package tools

import (
	"context"
	"fmt"
	"time"

	einotool "github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/wen/opentalon/internal/platform"
)

type scopeInput struct {
	ServiceID  string `json:"service_id,omitempty" jsonschema:"description=可选的服务ID过滤条件"`
	ToolName   string `json:"tool_name,omitempty" jsonschema:"description=可选的工具名称过滤条件"`
	RouteID    string `json:"route_id,omitempty" jsonschema:"description=可选的路由ID过滤条件"`
	ProviderID string `json:"provider_id,omitempty" jsonschema:"description=可选的Provider ID过滤条件"`
}

type rangeInput struct {
	From string `json:"from,omitempty" jsonschema:"description=可选的RFC3339查询开始时间，省略表示不限制开始时间"`
	To   string `json:"to,omitempty" jsonschema:"description=可选的RFC3339查询结束时间，省略表示查询到当前可见数据"`
}

type metricInput struct {
	scopeInput
	rangeInput
	Names []platform.MetricName `json:"names,omitempty" jsonschema:"description=可选的指标名称列表，省略表示查询全部指标"`
}

type logInput struct {
	scopeInput
	rangeInput
	Limit int `json:"limit,omitempty" jsonschema:"description=最多返回的日志条数，0表示使用平台默认值"`
}

type traceInput struct {
	scopeInput
	rangeInput
	Limit int `json:"limit,omitempty" jsonschema:"description=最多返回的Trace条数，0表示使用平台默认值"`
}

type changeInput struct {
	scopeInput
	rangeInput
}

type stateInput struct {
	scopeInput
}

type taskInput struct {
	scopeInput
	Statuses []platform.TaskStatus `json:"statuses,omitempty" jsonschema:"description=可选的异步任务状态列表"`
}

func buildStaticTools(service platform.ToolOpsPlatform, incidentID string) ([]einotool.InvokableTool, error) {
	builders := []func() (einotool.InvokableTool, error){
		func() (einotool.InvokableTool, error) {
			return toolutils.InferTool("query_metrics", "查询当前 Incident 的成功率、错误率、延迟、成本、鉴权错误率或连接错误率。先用指标确认异常范围和趋势。", func(ctx context.Context, input metricInput) (response[platform.MetricResult], error) {
				value, err := parseRange(input.rangeInput)
				if err != nil {
					return response[platform.MetricResult]{}, err
				}
				result, callErr := service.QueryMetrics(ctx, platform.MetricQuery{Scope: input.scope(incidentID), Range: value, Names: input.Names})
				return platformResponse(result, callErr), nil
			})
		},
		func() (einotool.InvokableTool, error) {
			return toolutils.InferTool("query_logs", "查询当前 Incident 中经过脱敏的日志。用于查找错误码、失败组件和修复或探测后出现的新证据。", func(ctx context.Context, input logInput) (response[[]platform.LogEntry], error) {
				value, err := parseRange(input.rangeInput)
				if err != nil {
					return response[[]platform.LogEntry]{}, err
				}
				result, callErr := service.QueryLogs(ctx, platform.LogQuery{Scope: input.scope(incidentID), Range: value, Limit: input.Limit})
				return platformResponse(result, callErr), nil
			})
		},
		func() (einotool.InvokableTool, error) {
			return toolutils.InferTool("query_traces", "查询当前 Incident 的脱敏 Trace 摘要。用于定位请求在哪个路由和调用阶段终止，以及是否到达 Provider。", func(ctx context.Context, input traceInput) (response[[]platform.TraceRecord], error) {
				value, err := parseRange(input.rangeInput)
				if err != nil {
					return response[[]platform.TraceRecord]{}, err
				}
				result, callErr := service.QueryTraces(ctx, platform.TraceQuery{Scope: input.scope(incidentID), Range: value, Limit: input.Limit})
				return platformResponse(result, callErr), nil
			})
		},
		func() (einotool.InvokableTool, error) {
			return toolutils.InferTool("get_services", "获取当前 Incident 涉及的服务、工具、SLO和关联路由目录。", func(ctx context.Context, input stateInput) (response[[]platform.Service], error) {
				result, callErr := service.GetServices(ctx, platform.StateQuery{Scope: input.scope(incidentID)})
				return platformResponse(result, callErr), nil
			})
		},
		func() (einotool.InvokableTool, error) {
			return toolutils.InferTool("get_routes", "获取当前路由权重、基线权重、Provider和可用状态。路由权重由控制器管理，Agent只能读取。", func(ctx context.Context, input stateInput) (response[[]platform.Route], error) {
				result, callErr := service.GetRoutes(ctx, platform.StateQuery{Scope: input.scope(incidentID)})
				return platformResponse(result, callErr), nil
			})
		},
		func() (einotool.InvokableTool, error) {
			return toolutils.InferTool("get_providers", "获取Provider健康状态、端点和兼容性元数据。不能读取Provider凭证。", func(ctx context.Context, input stateInput) (response[[]platform.Provider], error) {
				result, callErr := service.GetProviders(ctx, platform.StateQuery{Scope: input.scope(incidentID)})
				return platformResponse(result, callErr), nil
			})
		},
		func() (einotool.InvokableTool, error) {
			return toolutils.InferTool("get_config_versions", "获取当前配置版本、已知健康版本和激活状态。回滚前必须确认目标版本已知健康。", func(ctx context.Context, input stateInput) (response[[]platform.ConfigVersion], error) {
				result, callErr := service.GetConfigVersions(ctx, platform.StateQuery{Scope: input.scope(incidentID)})
				return platformResponse(result, callErr), nil
			})
		},
		func() (einotool.InvokableTool, error) {
			return toolutils.InferTool("get_change_records", "查询发布、配置和修复产生的审计记录。用于判断异常是否与近期变更相关。", func(ctx context.Context, input changeInput) (response[[]platform.ChangeRecord], error) {
				value, err := parseRange(input.rangeInput)
				if err != nil {
					return response[[]platform.ChangeRecord]{}, err
				}
				result, callErr := service.GetChangeRecords(ctx, platform.ChangeQuery{Scope: input.scope(incidentID), Range: value})
				return platformResponse(result, callErr), nil
			})
		},
		func() (einotool.InvokableTool, error) {
			return toolutils.InferTool("get_credential_metadata", "获取凭证ID、状态和管理方等元数据。此工具永远不会返回密钥或凭证值。", func(ctx context.Context, input stateInput) (response[[]platform.CredentialMetadata], error) {
				result, callErr := service.GetCredentialMetadata(ctx, platform.StateQuery{Scope: input.scope(incidentID)})
				return platformResponse(result, callErr), nil
			})
		},
		func() (einotool.InvokableTool, error) {
			return toolutils.InferTool("get_connection_metadata", "获取Provider连接池代次、DNS解析缓存代次、解析IP和连接状态。用于诊断连接失败。", func(ctx context.Context, input stateInput) (response[[]platform.ConnectionMetadata], error) {
				result, callErr := service.GetConnectionMetadata(ctx, platform.StateQuery{Scope: input.scope(incidentID)})
				return platformResponse(result, callErr), nil
			})
		},
		func() (einotool.InvokableTool, error) {
			return toolutils.InferTool("get_tasks", "查询当前 Incident 中修复、探测、恢复和升级操作对应的异步任务状态。", func(ctx context.Context, input taskInput) (response[[]platform.ManagedTask], error) {
				result, callErr := service.GetTasks(ctx, platform.TaskQuery{Scope: input.scope(incidentID), Statuses: input.Statuses})
				return platformResponse(result, callErr), nil
			})
		},
	}

	result := make([]einotool.InvokableTool, 0, len(builders)+4)
	for _, build := range builders {
		item, err := build()
		if err != nil {
			return nil, fmt.Errorf("build ToolOps query tool: %w", err)
		}
		result = append(result, item)
	}
	actions, err := buildActionTools(service, incidentID)
	if err != nil {
		return nil, err
	}
	return append(result, actions...), nil
}

func (input scopeInput) scope(incidentID string) platform.Scope {
	return platform.Scope{
		IncidentID: incidentID, ServiceID: input.ServiceID, ToolName: input.ToolName,
		RouteID: input.RouteID, ProviderID: input.ProviderID,
	}
}

func parseRange(input rangeInput) (platform.TimeRange, error) {
	from, err := parseOptionalTime("from", input.From)
	if err != nil {
		return platform.TimeRange{}, err
	}
	to, err := parseOptionalTime("to", input.To)
	if err != nil {
		return platform.TimeRange{}, err
	}
	if !from.IsZero() && !to.IsZero() && from.After(to) {
		return platform.TimeRange{}, fmt.Errorf("from must not be after to")
	}
	return platform.TimeRange{From: from, To: to}, nil
}

func parseOptionalTime(field, value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must use RFC3339: %w", field, err)
	}
	return parsed, nil
}
