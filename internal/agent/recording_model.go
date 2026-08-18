package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/wen/opentalon/internal/runartifact"
)

// recordingModel keeps model accounting available even when external tracing is disabled.
type recordingModel struct {
	next     model.ToolCallingChatModel
	recorder *runartifact.Recorder
	prepare  func(context.Context, []*schema.Message) ([]*schema.Message, runartifact.IncidentContextSnapshot, error)
	maxCalls int
	calls    *modelCallCounter
}

type modelCallCounter struct{ value atomic.Int64 }

func (m *recordingModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	next, err := m.next.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &recordingModel{next: next, recorder: m.recorder, prepare: m.prepare, maxCalls: m.maxCalls, calls: m.calls}, nil
}

func (m *recordingModel) reserveModelCall() error {
	if m.maxCalls <= 0 || m.calls == nil {
		return nil
	}
	if used := m.calls.value.Add(1); used > int64(m.maxCalls) {
		m.calls.value.Add(-1)
		return fmt.Errorf("model call limit %d exceeded", m.maxCalls)
	}
	return nil
}

func (m *recordingModel) Generate(ctx context.Context, input []*schema.Message, options ...model.Option) (*schema.Message, error) {
	if err := m.reserveModelCall(); err != nil {
		return nil, err
	}
	prepared, snapshot, err := m.prepare(ctx, input)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	message, err := m.next.Generate(ctx, prepared, options...)
	if m.recorder != nil {
		m.recorder.RecordModelCallWithContext(started, message, err, snapshot)
	}
	return message, err
}

func (m *recordingModel) Stream(ctx context.Context, input []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if err := m.reserveModelCall(); err != nil {
		return nil, err
	}
	prepared, snapshot, err := m.prepare(ctx, input)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	source, err := m.next.Stream(ctx, prepared, options...)
	if err != nil {
		if m.recorder != nil {
			m.recorder.RecordModelCallWithContext(started, nil, err, snapshot)
		}
		return nil, err
	}
	if m.recorder == nil {
		return source, nil
	}
	result, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer writer.Close()
		defer source.Close()
		var chunks []*schema.Message
		var streamErr error
		defer func() {
			message, concatErr := schema.ConcatMessages(chunks)
			if streamErr == nil {
				streamErr = concatErr
			}
			m.recorder.RecordModelCallWithContext(started, message, streamErr, snapshot)
		}()
		for {
			chunk, recvErr := source.Recv()
			if recvErr != nil {
				if !errors.Is(recvErr, io.EOF) {
					streamErr = recvErr
					writer.Send(nil, recvErr)
				}
				return
			}
			chunks = append(chunks, chunk)
			if writer.Send(chunk, nil) {
				return
			}
		}
	}()
	return result, nil
}
