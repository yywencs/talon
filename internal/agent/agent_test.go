package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/scenario"
	"github.com/wen/opentalon/internal/simulator"
)

func TestToolOpsAgentRunsReActWithIncidentTools(t *testing.T) {
	ctx := context.Background()
	service, incidentID := testSimulator(t)
	chatModel := &scriptedModel{}

	toolOpsAgent, err := NewToolOpsAgent(ctx, Config{
		Model:      chatModel,
		Platform:   service,
		IncidentID: incidentID,
	})
	require.NoError(t, err)
	assert.Equal(t, incidentID, toolOpsAgent.IncidentID())

	result, err := toolOpsAgent.Run(ctx, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "已完成服务范围确认，下一步查询异常指标。", result.Content)

	toolNames, inputs := chatModel.snapshot()
	assert.Contains(t, toolNames, "get_services")
	assert.Contains(t, toolNames, "query_metrics")
	assert.Contains(t, toolNames, "request_probe")
	assert.Contains(t, toolNames, "request_recovery")
	assert.Contains(t, toolNames, "escalate_incident")
	assert.Contains(t, toolNames, "rollback_mapping")
	require.Len(t, inputs, 2)

	for _, input := range inputs {
		require.NotEmpty(t, input)
		assert.Equal(t, schema.System, input[0].Role)
		assert.Contains(t, input[0].Content, incidentID)
		assert.Contains(t, input[0].Content, "accepted、pending 或 running 不代表操作成功")
	}
	assert.Contains(t, inputs[0][1].Content, "接管当前 Incident")
	assert.True(t, containsToolResult(inputs[1], "get_services", "image-service"))

	graph, options := toolOpsAgent.ExportGraph()
	assert.NotNil(t, graph)
	assert.NotEmpty(t, options)
}

func TestNewToolOpsAgentValidatesConfig(t *testing.T) {
	ctx := context.Background()
	service, incidentID := testSimulator(t)
	chatModel := &scriptedModel{}

	tests := []struct {
		name      string
		config    Config
		wantError string
	}{
		{name: "missing model", config: Config{Platform: service, IncidentID: incidentID}, wantError: "tool calling model is required"},
		{name: "missing platform", config: Config{Model: chatModel, IncidentID: incidentID}, wantError: "toolops platform is required"},
		{name: "missing incident", config: Config{Model: chatModel, Platform: service}, wantError: "incident ID is required"},
		{name: "negative max steps", config: Config{Model: chatModel, Platform: service, IncidentID: incidentID, MaxSteps: -1}, wantError: "max steps must not be negative"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := NewToolOpsAgent(ctx, test.config)
			assert.Nil(t, result)
			require.EqualError(t, err, test.wantError)
		})
	}
}

func TestToolOpsAgentAddsSystemMessageOnlyOnce(t *testing.T) {
	toolOpsAgent := &ToolOpsAgent{systemText: "toolops persona"}
	history := []*schema.Message{
		schema.UserMessage("继续调查"),
		schema.AssistantMessage("正在调查", nil),
	}

	prepared := toolOpsAgent.withSystemMessage(history)
	require.Len(t, prepared, 3)
	assert.Equal(t, schema.System, prepared[0].Role)
	assert.Equal(t, "toolops persona", prepared[0].Content)
	assert.Equal(t, history, prepared[1:])

	reused := toolOpsAgent.withSystemMessage(prepared)
	assert.Equal(t, prepared, reused)
	assert.Equal(t, 1, countSystemMessages(reused, "toolops persona"))
}

func testSimulator(t *testing.T) (*simulator.Simulator, string) {
	t.Helper()
	dataset, err := scenario.LoadDataset("../../data/toolops-v1")
	require.NoError(t, err)
	item, found := dataset.Find("mapping-regression-rollback-001")
	require.True(t, found)
	service, err := simulator.New(item.Scenario)
	require.NoError(t, err)
	return service, item.Scenario.Metadata.ID
}

func containsToolResult(messages []*schema.Message, toolName, content string) bool {
	for _, message := range messages {
		if message.Role == schema.Tool && message.ToolName == toolName && message.ToolCallID == "call-get-services" {
			return strings.Contains(message.Content, content)
		}
	}
	return false
}

func countSystemMessages(messages []*schema.Message, content string) int {
	count := 0
	for _, message := range messages {
		if message != nil && message.Role == schema.System && message.Content == content {
			count++
		}
	}
	return count
}

type scriptedModel struct {
	mu        sync.Mutex
	toolNames []string
	inputs    [][]*schema.Message
	calls     int
}

func (m *scriptedModel) WithTools(infos []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.toolNames = make([]string, 0, len(infos))
	for _, info := range infos {
		m.toolNames = append(m.toolNames, info.Name)
	}
	return m, nil
}

func (m *scriptedModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	m.calls++
	if m.calls == 1 {
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID: "call-get-services",
			Function: schema.FunctionCall{
				Name:      "get_services",
				Arguments: `{}`,
			},
		}}), nil
	}
	return schema.AssistantMessage("已完成服务范围确认，下一步查询异常指标。", nil), nil
}

func (m *scriptedModel) Stream(ctx context.Context, input []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, options...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (m *scriptedModel) snapshot() ([]string, [][]*schema.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := append([]string(nil), m.toolNames...)
	inputs := make([][]*schema.Message, len(m.inputs))
	for index := range m.inputs {
		inputs[index] = append([]*schema.Message(nil), m.inputs[index]...)
	}
	return names, inputs
}
