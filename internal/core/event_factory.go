package core

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wen/opentalon/internal/types"
)

// EventFactory 统一负责构造标准事件信封。
type EventFactory struct{}

func NewEventFactory() *EventFactory {
	return &EventFactory{}
}

// NewMessageEvent 将语义消息包装为标准 MessageEvent。
func (f *EventFactory) NewMessageEvent(msg types.Message, source types.EventSource) *types.MessageEvent {
	if !hasMessagePayload(msg) {
		return nil
	}
	msg.ToolCalls = nil
	return &types.MessageEvent{
		BaseEvent: types.BaseEvent{
			ID:        newEventID(),
			Timestamp: time.Now(),
			Source:    source,
		},
		Source:     source,
		LLMMessage: msg,
	}
}

// BuildActionEvents 将工具调用包装为标准 ActionEvent。
func (f *EventFactory) BuildActionEvents(calls []types.MessageToolCall, source types.EventSource) ([]*types.ActionEvent, error) {
	if len(calls) == 0 {
		return nil, nil
	}

	events := make([]*types.ActionEvent, 0, len(calls))
	for _, call := range calls {
		actionID := newEventID()
		metadata, err := parseToolMetadata(call.Arguments)
		if err != nil {
			return nil, fmt.Errorf("build action event for tool %s: %w", call.Name, err)
		}
		toolCall := call
		events = append(events, &types.ActionEvent{
			BaseEvent: types.BaseEvent{
				ID:        actionID,
				Timestamp: time.Now(),
				Source:    source,
			},
			ActionID:     actionID,
			ActionType:   actionTypeForToolCall(call.Name),
			ToolName:     call.Name,
			ToolCallID:   call.ID,
			ToolCall:     &toolCall,
			Summary:      metadata.Summary,
			SecurityRisk: metadata.SecurityRisk,
		})
	}
	return events, nil
}

// NewObservationEvent 将工具执行结果包装为标准 ObservationEvent。
func (f *EventFactory) NewObservationEvent(actionEvent *types.ActionEvent, observation types.Observation, toolName, toolCallID string) *types.ObservationEvent {
	if actionEvent == nil {
		return nil
	}
	return &types.ObservationEvent{
		BaseEvent: types.BaseEvent{
			ID:        newEventID(),
			Timestamp: time.Now(),
			Source:    types.SourceEnvironment,
		},
		ActionID:    actionEvent.ActionID,
		ToolName:    toolName,
		Observation: observation,
		ToolCallID:  toolCallID,
	}
}

func actionTypeForToolCall(toolName string) types.ActionType {
	if toolName == string(types.ActionFinish) {
		return types.ActionFinish
	}
	return types.ActionRun
}

func parseToolMetadata(arguments string) (types.ToolMetadata, error) {
	if strings.TrimSpace(arguments) == "" {
		return types.ToolMetadata{}, nil
	}

	var metadata types.ToolMetadata
	if err := json.Unmarshal([]byte(arguments), &metadata); err != nil {
		return types.ToolMetadata{}, fmt.Errorf("parse tool metadata: %w", err)
	}
	return metadata, nil
}

func hasMessagePayload(msg types.Message) bool {
	return len(msg.Content) > 0 ||
		msg.ReasoningContent != "" ||
		len(msg.ThinkingBlocks) > 0 ||
		len(msg.RedactedThinkingBlocks) > 0 ||
		msg.ResponsesReasoningItem != nil
}

func newEventID() string {
	return uuid.Must(uuid.NewV7()).String()
}
