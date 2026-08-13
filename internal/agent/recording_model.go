package agent

import (
	"context"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/wen/opentalon/internal/runartifact"
)

// recordingModel keeps model accounting available even when external tracing is disabled.
type recordingModel struct {
	next     model.ToolCallingChatModel
	recorder *runartifact.Recorder
}

func (m *recordingModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	next, err := m.next.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &recordingModel{next: next, recorder: m.recorder}, nil
}

func (m *recordingModel) Generate(ctx context.Context, input []*schema.Message, options ...model.Option) (*schema.Message, error) {
	started := time.Now()
	message, err := m.next.Generate(ctx, input, options...)
	m.recorder.RecordModelCall(started, message, err)
	return message, err
}

func (m *recordingModel) Stream(ctx context.Context, input []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return m.next.Stream(ctx, input, options...)
}
