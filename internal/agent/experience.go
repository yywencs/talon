package agent

import (
	"encoding/json"
	"github.com/wen/opentalon/internal/runartifact"
	"strings"
)

// Rehydrated results remain data in a user message and never become fresh evidence.
func experienceRecall(a runartifact.RunArtifact) string {
	out := []json.RawMessage{}
	size := 0
	for i := len(a.AgentRuns) - 1; i >= 0; i-- {
		calls := a.AgentRuns[i].ToolCalls
		for j := len(calls) - 1; j >= 0; j-- {
			c := calls[j]
			if (c.Name != "search_experiences" && c.Name != "get_experience") || c.Status != "succeeded" {
				continue
			}
			if size+len(c.Output) > 16000 {
				continue
			}
			size += len(c.Output)
			out = append(out, c.Output)
			if len(out) >= 3 {
				break
			}
		}
		if len(out) >= 3 {
			break
		}
	}
	if len(out) == 0 {
		return ""
	}
	b, _ := json.Marshal(out)
	return strings.Join([]string{"此前读取的历史经验（并非当前环境证据；新观察优先）：", string(b)}, "\n")
}
