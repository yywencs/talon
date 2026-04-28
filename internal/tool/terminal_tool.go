package tool

import (
	"context"

	terminalpkg "github.com/wen/opentalon/internal/tool/terminal"
)

type CmdOutputMetadata = terminalpkg.CmdOutputMetadata

type TerminalAction = terminalpkg.TerminalAction
type TerminalObservation = terminalpkg.TerminalObservation

func bashExecutor(ctx context.Context, action TerminalAction) *TerminalObservation {
	// TODO：之后需要多个paneID来实现一个agent对应多个terminal
	action.PaneID = "default_main"
	return terminalpkg.BashExecutor(ctx, action)
}

const TOOL_DESCRIPTION = terminalpkg.ToolDescription

func newBashTool() *BaseTool[TerminalAction, *TerminalObservation] {
	return &BaseTool[TerminalAction, *TerminalObservation]{
		ToolName: "bash",
		ToolDesc: TOOL_DESCRIPTION,
		Executor: bashExecutor,
	}
}

func init() {
	Register("bash", func(ctx context.Context) Tool {
		return newBashTool()
	})
}
