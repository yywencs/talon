package tool

import (
	"context"

	"github.com/wen/opentalon/internal/types"
)

type FinishAction struct {
	ActionMetadata
	Message string `json:"message" jsonschema:"description=任务完成的总结信息,required"`
	Success bool   `json:"success" jsonschema:"description=任务是否成功完成,required"`
}

func (a FinishAction) ActionType() types.ActionType {
	return types.ActionFinish
}

func (a FinishAction) GetBase() *types.BaseEvent {
	return nil
}

func (a FinishAction) Kind() string {
	return "finish"
}

func (a FinishAction) Name() string {
	return "finish_action"
}

type FinishObservation struct {
	types.BaseObservation
}

func (o *FinishObservation) GetContent() []types.Content {
	return o.BaseObservation.Content
}

func finishExecutor(ctx context.Context, action FinishAction) *FinishObservation {
	return &FinishObservation{}
}

func newFinishTool() *BaseTool[FinishAction, *FinishObservation] {
	return &BaseTool[FinishAction, *FinishObservation]{
		ToolName: "finish",
		ToolDesc: "结束当前任务并返回结果。当任务成功完成或遇到无法继续的错误时必须调用此工具。",
		Executor: finishExecutor,
	}
}

func NewFinishTool() *BaseTool[FinishAction, *FinishObservation] {
	return newFinishTool()
}

func init() {
	Register("finish", func(ctx context.Context) Tool {
		return newFinishTool()
	})
}
