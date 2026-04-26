package tool

import (
	"context"

	terminalpkg "github.com/wen/opentalon/internal/tool/terminal"
)

type CmdOutputMetadata = terminalpkg.CmdOutputMetadata

type TerminalAction = terminalpkg.TerminalAction
type TerminalObservation = terminalpkg.TerminalObservation
type BashTool = terminalpkg.BashTool

func bashExecutor(ctx context.Context, action BashTool) *TerminalObservation {
	return terminalpkg.BashExecutor(ctx, action)
}

const TOOL_DESCRIPTION = terminalpkg.ToolDescription

func newBashTool() *BaseTool[BashTool, *TerminalObservation] {
	return &BaseTool[BashTool, *TerminalObservation]{
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
