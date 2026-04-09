package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/config"
	"github.com/wen/opentalon/pkg/prompts"
)

type Agent interface {
	Step(ctx context.Context, state *types.State) (types.Action, error)
}

type BaseAgent struct {
	client    LLMClient
	promptReg *prompts.Registry
	promptBld *PromptBuilder
	model     string
}

func NewBaseAgent(cfg config.LLMConfig) (*BaseAgent, error) {
	client, err := NewLLMClient(cfg)
	if err != nil {
		return nil, err
	}
	return &BaseAgent{
		client:    client,
		promptReg: prompts.NewRegistry(cfg.PromptsDir),
		promptBld: NewPromptBuilder(),
		model:     cfg.Model,
	}, nil
}

func (a *BaseAgent) Step(ctx context.Context, state *types.State) (types.Action, error) {
	systemPrompt := a.promptReg.Get("system_prompt").Base()
	userPrompt := a.promptReg.Get("user_prompt").Base()
	return stepWithLLM(ctx, a.client, a.promptBld, a.model, state, systemPrompt, userPrompt)
}

func stepWithLLM(ctx context.Context, client LLMClient, promptBld *PromptBuilder, model string, state *types.State, systemPrompt string, userPromptExamples string) (types.Action, error) {
	req := ChatRequest{
		Model:    model,
		Messages: promptBld.BuildMessages(state, systemPrompt, userPromptExamples),
	}

	resp, err := client.Chat(ctx, req)
	if err != nil {
		return nil, err
	}

	action := responseToAction(resp)
	if action == nil {
		return nil, fmt.Errorf("模型未输出有效动作")
	}

	action.GetBase().LLMMetrics = &types.Metrics{
		PromptTokens:     resp.PromptTokens,
		CompletionTokens: resp.CompletionTokens,
	}
	return action, nil
}

func responseToAction(resp *ChatResponse) types.Action {
	content := strings.TrimSpace(resp.Content)

	if finish := extractTag(content, "finish"); finish != "" || strings.Contains(content, "<finish>") {
		return &types.FinishAction{Result: strings.TrimSpace(finish)}
	}

	if bash := extractTag(content, "execute_bash"); bash != "" {
		return &types.CmdRunAction{Command: strings.TrimSpace(bash)}
	}

	if python := extractTag(content, "execute_ipython"); python != "" {
		return &types.CmdRunAction{Command: "python3 -c " + strings.TrimSpace(python)}
	}

	if browse := extractTag(content, "execute_browse"); browse != "" {
		return &types.CmdRunAction{Command: "echo 'browse: " + strings.TrimSpace(browse) + "'"}
	}

	if content != "" {
		return &types.MessageAction{Content: content}
	}

	return nil
}

func extractTag(content, tagName string) string {
	patterns := []string{
		fmt.Sprintf(`<%s>(?s)(.+?)</%s>`, tagName, tagName),
		fmt.Sprintf(`<%s>(.+)</%s>`, tagName, tagName),
	}
	for _, pattern := range patterns {
		re := regexp.MustCompile(`(?i)` + pattern)
		matches := re.FindStringSubmatch(content)
		if len(matches) > 1 {
			return strings.TrimSpace(matches[1])
		}
	}
	return ""
}
