package agentreview

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
	agentpkg "github.com/wen/opentalon/internal/agent"
	"github.com/wen/opentalon/internal/types"
)

type recordingLLMClient struct {
	request agentpkg.ChatRequest
}

func (c *recordingLLMClient) Chat(_ context.Context, request agentpkg.ChatRequest) (*agentpkg.ChatResponse, error) {
	c.request = request
	return &agentpkg.ChatResponse{Message: types.Message{
		Role: types.RoleAssistant,
		ToolCalls: []types.MessageToolCall{{
			ID: "call_search_2", Name: searchRepositoryToolName,
			Arguments: `{"revision":"head","symbol":"sanitize"}`, Origin: types.OriginCompletion,
		}},
	}}, nil
}

func (c *recordingLLMClient) StreamChat(_ context.Context, _ agentpkg.ChatRequest, _ func(string)) (*agentpkg.ChatResponse, error) {
	return nil, nil
}

func TestTalonChatModelBridgesEinoToolCallingProtocol(t *testing.T) {
	client := &recordingLLMClient{}
	base := &talonChatModel{client: client, model: "test-model"}
	repositoryTools, err := NewRepositoryTools(&fakeRepositoryReader{})
	require.NoError(t, err)
	toolInfo, err := repositoryTools[0].Info(context.Background())
	require.NoError(t, err)

	bound, err := base.WithTools([]*schema.ToolInfo{toolInfo})
	require.NoError(t, err)
	response, err := bound.Generate(context.Background(), []*schema.Message{
		schema.SystemMessage("review"),
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID: "call_read_1", Type: "function",
				Function: schema.FunctionCall{Name: readRepositoryFileToolName, Arguments: `{"revision":"head"}`},
			}},
		},
		schema.ToolMessage(`{"path":"safe.go"}`, "call_read_1", schema.WithToolName(readRepositoryFileToolName)),
	}, model.WithTemperature(0.25))
	require.NoError(t, err)

	require.Equal(t, "test-model", client.request.Model)
	require.InDelta(t, 0.25, client.request.Temperature, 0.0001)
	require.Len(t, client.request.Tools, 1)
	function, ok := client.request.Tools[0]["function"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, readRepositoryFileToolName, function["name"])
	require.NotNil(t, function["parameters"])
	require.Len(t, client.request.Messages, 3)
	require.Equal(t, "call_read_1", client.request.Messages[1].ToolCalls[0].ID)
	require.Equal(t, "call_read_1", client.request.Messages[2].ToolCallID)
	require.Equal(t, readRepositoryFileToolName, client.request.Messages[2].Name)

	require.Len(t, response.ToolCalls, 1)
	require.Equal(t, "call_search_2", response.ToolCalls[0].ID)
	require.Equal(t, searchRepositoryToolName, response.ToolCalls[0].Function.Name)
}
