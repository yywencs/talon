// Package agentreview 使用 Eino 编排单模型和带只读工具的代码审查流程。
// 它实现 review.Reviewer，但不改变 review 包的输入输出契约。
package agentreview

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/wen/opentalon/internal/review"
	"github.com/wen/opentalon/internal/tool/repository"
)

const (
	reviewerName           = "eino-agent-reviewer/v1"
	repositoryReviewerName = "eino-agent-reviewer/v2-repository-tools"
)

type reviewInput struct {
	Request review.Request
	Files   []review.ChangedFile
}

// Reviewer 将 Review 请求转换为结构化 Findings。runnable 保留无工具的兼容路径，
// agent 则承载带只读仓库工具的 Eino ReAct 路径，两者只会初始化一个。
type Reviewer struct {
	runnable compose.Runnable[reviewInput, []review.Finding]
	agent    *react.Agent
	name     string
}

// New 编译 Eino Review Chain。chatModel 由调用方注入，便于生产环境切换
// Ollama/OpenAI-compatible，也便于测试使用完全离线的 Fake ChatModel。
func New(ctx context.Context, chatModel model.BaseChatModel) (*Reviewer, error) {
	if chatModel == nil {
		return nil, fmt.Errorf("agentreview: chat model is required")
	}
	runnable, err := compileChain(ctx, chatModel)
	if err != nil {
		return nil, fmt.Errorf("agentreview: compile Eino chain: %w", err)
	}
	return &Reviewer{runnable: runnable, name: reviewerName}, nil
}

// NewWithRepository 创建带只读仓库工具的 Reviewer。模型先分析 Diff，需要更多证据时
// 可以读取 base/head 文件、搜索符号或列出文件，最终仍必须输出结构化 Findings。
func NewWithRepository(ctx context.Context, chatModel model.ToolCallingChatModel, reader repository.Reader) (*Reviewer, error) {
	if chatModel == nil {
		return nil, fmt.Errorf("agentreview: tool-calling chat model is required")
	}
	tools, err := NewRepositoryTools(reader)
	if err != nil {
		return nil, err
	}
	baseTools := make([]interfaceTool, len(tools))
	for index := range tools {
		baseTools[index] = tools[index]
	}
	agent, err := compileRepositoryAgent(ctx, chatModel, baseTools)
	if err != nil {
		return nil, fmt.Errorf("agentreview: compile repository agent: %w", err)
	}
	return &Reviewer{agent: agent, name: repositoryReviewerName}, nil
}

// Name 返回可写入 Report 和评测结果的稳定实现版本。
func (r *Reviewer) Name() string {
	if r == nil || r.name == "" {
		return reviewerName
	}
	return r.name
}

// Review 调用对应的 Eino Chain 或 Agent。Diff 已由上层 Service 解析，因此这里同时
// 获得原始元数据和结构化文件/Hunk/行号，但不会获得漏洞库的标准答案。
func (r *Reviewer) Review(ctx context.Context, request review.Request, files []review.ChangedFile) ([]review.Finding, error) {
	if r == nil || (r.runnable == nil && r.agent == nil) {
		return nil, fmt.Errorf("agentreview: reviewer is not initialized")
	}
	input := reviewInput{Request: request, Files: files}
	var findings []review.Finding
	var err error
	if r.agent != nil {
		messages, buildErr := buildRepositoryAgentMessages(ctx, input)
		if buildErr != nil {
			return nil, fmt.Errorf("agentreview: build repository agent messages: %w", buildErr)
		}
		message, generateErr := r.agent.Generate(ctx, messages)
		if generateErr != nil {
			return nil, fmt.Errorf("agentreview: invoke repository agent: %w", generateErr)
		}
		findings, err = parseModelMessage(ctx, message)
	} else {
		findings, err = r.runnable.Invoke(ctx, input)
	}
	if err != nil {
		return nil, fmt.Errorf("agentreview: decode review result: %w", err)
	}
	findings, err = validateFindings(findings, files)
	if err != nil {
		return nil, err
	}
	return findings, nil
}
