package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/wen/opentalon/internal/agent"
	"github.com/wen/opentalon/internal/controller"
	"github.com/wen/opentalon/internal/workflow"
)

func printTransitions(printer *safePrinter, snapshot workflow.Snapshot, seen *uint64) {
	for _, transition := range snapshot.History {
		if transition.Version <= *seen {
			continue
		}
		printer.printf("[workflow] v%d %s -> %s event=%s actor=%s reason=%s\n",
			transition.Version, transition.From, transition.To, transition.Event, transition.Actor, transition.Reason)
		*seen = transition.Version
	}
}

func printSummary(printer *safePrinter, result Result) {
	snapshot := result.Controller.Snapshot
	printer.printf("[result] reason=%s state=%s advances=%d transitions=%d\n",
		result.Controller.Reason, snapshot.State, result.Controller.Advances, len(snapshot.History))
	if snapshot.ExecutionIntent != nil {
		actionCount := 0
		for _, stage := range snapshot.ExecutionIntent.Stages {
			actionCount += len(stage.Actions)
		}
		printer.printf("[intent] id=%s root_cause=%s stages=%d actions=%d\n",
			snapshot.ExecutionIntent.ID, snapshot.ExecutionIntent.RootCause, len(snapshot.ExecutionIntent.Stages), actionCount)
	}
	routeIDs := make([]string, 0, len(result.World.Routes))
	for id := range result.World.Routes {
		routeIDs = append(routeIDs, id)
	}
	sort.Strings(routeIDs)
	for _, id := range routeIDs {
		route := result.World.Routes[id]
		printer.printf("[route] id=%s weight=%d baseline=%d enabled=%t\n", id, route.Weight, route.BaselineWeight, route.Enabled)
	}
}

func mapText(values map[string]any, key string) string {
	if values == nil || values[key] == nil {
		return "-"
	}
	encoded, err := json.Marshal(values[key])
	if err != nil {
		return "?"
	}
	return strings.Trim(string(encoded), `"`)
}

type printingInvestigator struct {
	agent   *agent.ToolOpsAgent
	printer *safePrinter
}

func (p *printingInvestigator) IncidentID() string { return p.agent.IncidentID() }

func (p *printingInvestigator) Investigate(ctx context.Context, instruction string) error {
	p.printer.printf("[agent] instruction=%s\n", instruction)
	message, err := p.agent.Run(ctx, instruction)
	if err != nil {
		return err
	}
	if message != nil && strings.TrimSpace(message.Content) != "" {
		p.printer.printf("[agent] response:\n%s\n", message.Content)
	}
	return nil
}

type safePrinter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (p *safePrinter) printf(format string, values ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = fmt.Fprintf(p.writer, format, values...)
}

var _ controller.Investigator = (*printingInvestigator)(nil)
