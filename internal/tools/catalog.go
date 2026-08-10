// Package tools 将平台能力包装为每个 Incident 独立的 Eino 工具集。
package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/wen/opentalon/internal/platform"
)

// Set 保存一个 Incident 可见的工具，不使用 Coding Agent 的全局 Registry。
type Set struct {
	tools     []einotool.BaseTool
	invokable map[string]einotool.InvokableTool
}

// New 构建只绑定到指定 Incident 和 Platform 实例的安全工具集。
func New(ctx context.Context, service platform.ToolOpsPlatform, incidentID string) (*Set, error) {
	if service == nil {
		return nil, fmt.Errorf("toolops platform is required")
	}
	incidentID = strings.TrimSpace(incidentID)
	if incidentID == "" {
		return nil, fmt.Errorf("incident ID is required")
	}

	staticTools, err := buildStaticTools(service, incidentID)
	if err != nil {
		return nil, err
	}
	capabilities, err := service.GetRemediationCapabilities(ctx, platform.StateQuery{
		Scope: platform.Scope{IncidentID: incidentID},
	})
	if err != nil {
		return nil, fmt.Errorf("get remediation capabilities: %w", err)
	}
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i].Name < capabilities[j].Name })

	set := &Set{invokable: make(map[string]einotool.InvokableTool, len(staticTools)+len(capabilities))}
	for _, item := range staticTools {
		if err := set.add(ctx, item); err != nil {
			return nil, err
		}
	}
	for _, capability := range capabilities {
		if err := set.add(ctx, newRemediationTool(service, incidentID, capability)); err != nil {
			return nil, err
		}
	}
	return set, nil
}

func (s *Set) add(ctx context.Context, item einotool.InvokableTool) error {
	info, err := item.Info(ctx)
	if err != nil {
		return fmt.Errorf("read tool info: %w", err)
	}
	if info == nil || strings.TrimSpace(info.Name) == "" {
		return fmt.Errorf("tool name is required")
	}
	if strings.TrimSpace(info.Desc) == "" {
		return fmt.Errorf("tool %q description is required", info.Name)
	}
	if _, exists := s.invokable[info.Name]; exists {
		return fmt.Errorf("duplicate tool name %q", info.Name)
	}
	s.invokable[info.Name] = item
	s.tools = append(s.tools, item)
	return nil
}

// Tools 返回可直接传给 Eino ReAct Agent 或 ToolsNode 的工具列表副本。
func (s *Set) Tools() []einotool.BaseTool {
	if s == nil {
		return nil
	}
	return append([]einotool.BaseTool(nil), s.tools...)
}

// Resolve 根据模型调用使用的函数名取得可执行工具。
func (s *Set) Resolve(name string) (einotool.InvokableTool, bool) {
	if s == nil {
		return nil, false
	}
	item, ok := s.invokable[name]
	return item, ok
}

type response[T any] struct {
	Data  T      `json:"data"`
	Error string `json:"error,omitempty"`
}

func platformResponse[T any](data T, err error) response[T] {
	result := response[T]{Data: data}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}
