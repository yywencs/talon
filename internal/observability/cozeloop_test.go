package observability

import (
	"context"
	"sync"
	"testing"
	"time"

	loopcallback "github.com/cloudwego/eino-ext/callbacks/cozeloop"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	cozeloop "github.com/coze-dev/cozeloop-go"
	"github.com/coze-dev/cozeloop-go/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type captureExporter struct {
	mu    sync.Mutex
	spans []*entity.UploadSpan
}

func (e *captureExporter) ExportSpans(_ context.Context, spans []*entity.UploadSpan) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.spans = append(e.spans, spans...)
	return nil
}

func (e *captureExporter) ExportFiles(_ context.Context, _ []*entity.UploadFile) error {
	return nil
}

func (e *captureExporter) snapshot() []*entity.UploadSpan {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]*entity.UploadSpan(nil), e.spans...)
}

func TestIncidentCallbackAgentAndEinoSpansUseOneRedactedTrace(t *testing.T) {
	require.NoError(t, Shutdown(context.Background()))
	exporter := &captureExporter{}
	client, err := cozeloop.NewClient(
		cozeloop.WithAPIBaseURL("http://127.0.0.1:1"),
		cozeloop.WithWorkspaceID("workspace-observability-test"),
		cozeloop.WithAPIToken("token-observability-test"),
		cozeloop.WithExporter(exporter),
		cozeloop.WithTraceQueueConf(&cozeloop.TraceQueueConf{
			SpanQueueLength:          32,
			SpanMaxExportBatchLength: 1,
		}),
	)
	require.NoError(t, err)

	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.WorkspaceID = "workspace-observability-test"
	cfg.ServiceName = "talon-observability-test"
	redactor, err := NewRedactor(cfg.RedactionRules)
	require.NoError(t, err)
	parser := newSafeDataParser(loopcallback.NewDefaultDataParser(true), redactor)
	handler := newSafeEinoHandler(loopcallback.NewLoopHandler(client,
		loopcallback.WithCallbackDataParser(parser),
		loopcallback.WithAggrMessageOutput(true),
	), redactor)

	globalProvider.mu.Lock()
	globalProvider.cfg = cfg
	globalProvider.client = client
	globalProvider.handler = handler
	globalProvider.redactor = redactor
	globalProvider.mu.Unlock()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, Shutdown(shutdownCtx))
	})

	ctx, finishIncident := BeginCallback(context.Background(), "toolops.incident.run", map[string]any{
		"incident_id": "incident-001",
	})
	agentCtx, run := StartAgentRun(ctx, "incident-001", "run", "investigating", map[string]any{
		"authorization": "Bearer agent-root-secret",
	})
	info := &callbacks.RunInfo{Name: "ToolOpsModel", Type: "OpenAI", Component: components.ComponentOfChatModel}
	modelCtx := handler.OnStart(agentCtx, info, &model.CallbackInput{
		Messages: []*schema.Message{schema.UserMessage("Authorization: Bearer model-input-secret")},
		Config:   &model.Config{Model: "deepseek-test"},
	})
	handler.OnEnd(modelCtx, info, &model.CallbackOutput{
		Message: schema.AssistantMessage("api_key=output-secret", nil),
		TokenUsage: &model.TokenUsage{
			PromptTokens:     11,
			CompletionTokens: 7,
			TotalTokens:      18,
		},
	})
	FinishAgentRun(run, nil, "validating", []WorkflowTransition{{
		From: "investigating", To: "validating", Event: "execution_intent_submitted", Actor: "agent", Version: 2,
	}}, map[string]any{"result": "Bearer agent-output-secret"})
	_, err = RunCallback(ctx, "toolops.intent.dry_run", map[string]any{"intent_id": "intent-001"},
		func(context.Context) (map[string]any, error) {
			return map[string]any{"status": "succeeded"}, nil
		})
	require.NoError(t, err)
	finishIncident(map[string]any{"state": "resolved"}, nil)

	flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client.Flush(flushCtx)
	require.NoError(t, flushCtx.Err())

	spans := exporter.snapshot()
	require.Len(t, spans, 4)
	var incident, agentRun, dryRun, chat *entity.UploadSpan
	for _, span := range spans {
		switch span.SpanName {
		case "toolops.incident.run":
			incident = span
		case "toolops.agent.run":
			agentRun = span
		case "toolops.intent.dry_run":
			dryRun = span
		case "ToolOpsModel":
			chat = span
		}
	}
	require.NotNil(t, incident)
	require.NotNil(t, agentRun)
	require.NotNil(t, dryRun)
	require.NotNil(t, chat)
	assert.Equal(t, "Lambda", incident.SpanType)
	assert.Equal(t, "agent", agentRun.SpanType)
	assert.Equal(t, incident.TraceID, agentRun.TraceID)
	assert.Equal(t, incident.SpanID, agentRun.ParentID)
	assert.Equal(t, incident.TraceID, dryRun.TraceID)
	assert.Equal(t, incident.SpanID, dryRun.ParentID)
	assert.Equal(t, agentRun.TraceID, chat.TraceID)
	assert.Equal(t, agentRun.SpanID, chat.ParentID)
	assert.Equal(t, "talon-observability-test", agentRun.ServiceName)
	assert.Contains(t, agentRun.Input, defaultRedactionReplacement)
	assert.Contains(t, agentRun.Output, defaultRedactionReplacement)
	assert.NotContains(t, agentRun.Input+agentRun.Output, "agent-root-secret")
	assert.NotContains(t, agentRun.Input+agentRun.Output, "agent-output-secret")
	assert.Contains(t, chat.Input, defaultRedactionReplacement)
	assert.Contains(t, chat.Output, defaultRedactionReplacement)
	assert.NotContains(t, chat.Input+chat.Output, "model-input-secret")
	assert.NotContains(t, chat.Input+chat.Output, "output-secret")
	assert.Equal(t, int64(11), chat.TagsLong["input_tokens"])
	assert.Equal(t, int64(7), chat.TagsLong["output_tokens"])
	assert.Equal(t, int64(18), chat.TagsLong["tokens"])
	assert.Positive(t, chat.TagsLong["latency_first_resp"])
}

func TestSafeTraceValueRestoresNativeNumberTypes(t *testing.T) {
	redactor, err := NewRedactor(DefaultRedactionRules())
	require.NoError(t, err)

	value := safeTraceValue(redactor, "tags", map[string]any{
		"tokens": 18,
		"ratio":  0.25,
		"nested": []any{int32(7), 1.5},
	})

	tags, ok := value.(map[string]any)
	require.True(t, ok)
	assert.IsType(t, int64(0), tags["tokens"])
	assert.Equal(t, int64(18), tags["tokens"])
	assert.IsType(t, float64(0), tags["ratio"])
	nested, ok := tags["nested"].([]any)
	require.True(t, ok)
	assert.IsType(t, int64(0), nested[0])
	assert.IsType(t, float64(0), nested[1])
}
