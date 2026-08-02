package agentreview

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	agentpkg "github.com/wen/opentalon/internal/agent"
	"github.com/wen/opentalon/internal/tool/repository"
	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/config"
	"github.com/wen/opentalon/pkg/utils"
)

// talonChatModel 将 OpenTalon 已有的多 Provider LLMClient 适配为 Eino
// ToolCallingChatModel，并桥接工具定义、工具调用和工具结果消息。
type talonChatModel struct {
	client agentpkg.LLMClient
	model  string
	tools  []*schema.ToolInfo
}

// NewFromConfig 复用 OpenTalon 的 Ollama/OpenAI-compatible 配置构造 Eino Reviewer。
func NewFromConfig(ctx context.Context, cfg config.LLMConfig) (*Reviewer, error) {
	chatModel, err := newTalonChatModel(cfg)
	if err != nil {
		return nil, err
	}
	return New(ctx, chatModel)
}

// NewFromConfigWithRepository 创建可以调用请求级只读仓库工具的 Eino Reviewer。
func NewFromConfigWithRepository(ctx context.Context, cfg config.LLMConfig, reader repository.Reader) (*Reviewer, error) {
	chatModel, err := newTalonChatModel(cfg)
	if err != nil {
		return nil, err
	}
	return NewWithRepository(ctx, chatModel, reader)
}

func newTalonChatModel(cfg config.LLMConfig) (*talonChatModel, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("agentreview: LLM model is required")
	}
	client, err := agentpkg.NewLLMClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("agentreview: create LLM client: %w", err)
	}
	return &talonChatModel{client: client, model: cfg.Model}, nil
}

// WithTools 返回绑定工具定义的模型副本，避免并发 Review 之间互相覆盖工具列表。
func (m *talonChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	if m == nil || m.client == nil {
		return nil, fmt.Errorf("agentreview: Talon chat model is not initialized")
	}
	if _, err := einoToolsToOpenAI(tools); err != nil {
		return nil, err
	}
	clone := *m
	clone.tools = append([]*schema.ToolInfo(nil), tools...)
	return &clone, nil
}

func (m *talonChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	messages, err := toTalonMessages(input)
	if err != nil {
		return nil, err
	}
	options := model.GetCommonOptions(&model.Options{Tools: m.tools}, opts...)
	tools, err := einoToolsToOpenAI(options.Tools)
	if err != nil {
		return nil, err
	}
	modelName := m.model
	if options.Model != nil && *options.Model != "" {
		modelName = *options.Model
	}
	temperature := float64(0)
	if options.Temperature != nil {
		temperature = float64(*options.Temperature)
	}
	response, err := m.client.Chat(ctx, agentpkg.ChatRequest{
		Model: modelName, Messages: messages, Temperature: temperature, Stream: false, Tools: tools,
	})
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, fmt.Errorf("agentreview: LLM client returned nil response")
	}
	content := utils.FlattenTextContent(response.Message.Content)
	message := schema.AssistantMessage(content, nil)
	for _, toolCall := range response.Message.ToolCalls {
		message.ToolCalls = append(message.ToolCalls, schema.ToolCall{
			ID: toolCall.ID, Type: "function",
			Function: schema.FunctionCall{Name: toolCall.Name, Arguments: toolCall.Arguments},
		})
	}
	return message, nil
}

func (m *talonChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func toTalonMessages(messages []*schema.Message) ([]types.Message, error) {
	result := make([]types.Message, 0, len(messages))
	for index, message := range messages {
		if message == nil {
			return nil, fmt.Errorf("agentreview: Eino message %d is nil", index)
		}
		role, err := toTalonRole(message.Role)
		if err != nil {
			return nil, fmt.Errorf("agentreview: Eino message %d: %w", index, err)
		}
		converted := types.Message{Role: role, ToolCallID: message.ToolCallID, Name: message.ToolName}
		if message.Content != "" {
			converted.Content = []types.Content{types.TextContent{Text: message.Content}}
		}
		for _, toolCall := range message.ToolCalls {
			converted.ToolCalls = append(converted.ToolCalls, types.MessageToolCall{
				ID: toolCall.ID, Name: toolCall.Function.Name,
				Arguments: toolCall.Function.Arguments, Origin: types.OriginCompletion,
			})
		}
		result = append(result, converted)
	}
	return result, nil
}

// einoToolsToOpenAI 将 Eino ToolInfo 转换为 Talon provider adapter 使用的
// OpenAI function calling wire 结构。Ollama 也接受相同的 tools 定义。
func einoToolsToOpenAI(tools []*schema.ToolInfo) ([]map[string]any, error) {
	result := make([]map[string]any, 0, len(tools))
	seen := make(map[string]struct{}, len(tools))
	for index, toolInfo := range tools {
		if toolInfo == nil || toolInfo.Name == "" {
			return nil, fmt.Errorf("agentreview: Eino tool %d has no name", index)
		}
		if _, exists := seen[toolInfo.Name]; exists {
			return nil, fmt.Errorf("agentreview: duplicate Eino tool name %q", toolInfo.Name)
		}
		seen[toolInfo.Name] = struct{}{}
		parameters := map[string]any{"type": "object", "properties": map[string]any{}}
		if toolInfo.ParamsOneOf != nil {
			jsonSchema, err := toolInfo.ParamsOneOf.ToJSONSchema()
			if err != nil {
				return nil, fmt.Errorf("agentreview: convert tool %q schema: %w", toolInfo.Name, err)
			}
			encoded, err := json.Marshal(jsonSchema)
			if err != nil {
				return nil, fmt.Errorf("agentreview: marshal tool %q schema: %w", toolInfo.Name, err)
			}
			if err := json.Unmarshal(encoded, &parameters); err != nil {
				return nil, fmt.Errorf("agentreview: decode tool %q schema: %w", toolInfo.Name, err)
			}
		}
		result = append(result, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": toolInfo.Name, "description": toolInfo.Desc, "parameters": parameters,
			},
		})
	}
	return result, nil
}

func toTalonRole(role schema.RoleType) (types.MessageRole, error) {
	switch role {
	case schema.System:
		return types.RoleSystem, nil
	case schema.User:
		return types.RoleUser, nil
	case schema.Assistant:
		return types.RoleAssistant, nil
	case schema.Tool:
		return types.RoleTool, nil
	default:
		return "", fmt.Errorf("unsupported role %q", role)
	}
}
