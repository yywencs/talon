package terminal

import "github.com/wen/opentalon/internal/types"

func errorOutput(command, workingDir string, pid *int, timeout bool, exitCode int, msg string) *TerminalObservation {
	return NewTerminalObservation(command, workingDir, pid, timeout, exitCode, msg)
}

// NewTerminalObservation 构造终端工具的 observation 结果。
func NewTerminalObservation(command, workingDir string, pid *int, timeout bool, exitCode int, output string) *TerminalObservation {
	obs := &TerminalObservation{
		BaseObservation: types.BaseObservation{
			BaseEvent: types.BaseEvent{
				Source: types.SourceEnvironment,
			},
			Content: []types.Content{
				types.TextContent{
					Text: output,
				},
			},
			ErrorStatus: timeout || exitCode != 0,
		},
		Command:  stringPtr(command),
		Timeout:  timeout,
		Metadata: CmdOutputMetadata{PID: pid, WorkingDir: workingDir},
	}
	if exitCode != 0 || output != "" || pid != nil {
		obs.ExitCode = intPtr(exitCode)
	}
	return obs
}

func intPtr(v int) *int {
	return &v
}

func stringPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
