package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/approval"
	"github.com/wen/opentalon/internal/controller"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/runlease"
	"github.com/wen/opentalon/internal/runmeta"
	"github.com/wen/opentalon/internal/scenario"
	"github.com/wen/opentalon/internal/simulator"
	"github.com/wen/opentalon/internal/simulatorhttp"
	"github.com/wen/opentalon/internal/storage"
	"github.com/wen/opentalon/internal/workflow"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The same test binary provides separate Simulator and Agent OS processes.
// The response barrier is after server acceptance but before the Agent receives ID.
func TestHTTPProcessHelper(t *testing.T) {
	mode := os.Getenv("TALON_PROCESS_HELPER")
	if mode == "" {
		t.Skip("subprocess helper")
	}
	ctx := context.Background()
	if mode == "server" {
		ds, e := scenario.LoadDataset(testDatasetRoot(t))
		require.NoError(t, e)
		item, _ := ds.Find("mapping-regression-rollback-001")
		item.Scenario.Timeline = append(item.Scenario.Timeline, scenario.TimelineEvent{At: "6m30s", Event: "provider.endpoint.change", Target: item.Scenario.InitialState.Providers[0].ID, Values: map[string]any{"endpoint": "https://new-fault.invalid", "internal_effect": map[string]any{"success_rate": 0.4, "error_type": "connection_refused"}}})
		sim, e := simulator.New(item.Scenario)
		require.NoError(t, e)
		require.NoError(t, sim.Advance(ctx, 5*time.Minute))
		s, e := simulatorhttp.NewServer(sim, ds.Version)
		require.NoError(t, e)
		release := make(chan struct{})
		h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/ExecuteRemediation" {
				var body map[string]json.RawMessage
				raw := json.NewDecoder(r.Body)
				require.NoError(t, raw.Decode(&body))
				var q platform.RemediationRequest
				require.NoError(t, json.Unmarshal(body["payload"], &q))
				encoded, _ := json.Marshal(body)
				r.Body = io.NopCloser(strings.NewReader(string(encoded)))
				if !q.DryRun {
					rec := httptest.NewRecorder()
					s.ServeHTTP(rec, r)
					fmt.Printf("ACCEPTED %s\n", rec.Body.String())
					<-release
					for k, v := range rec.Header() {
						w.Header()[k] = v
					}
					w.WriteHeader(rec.Code)
					w.Write(rec.Body.Bytes())
					return
				}
			}
			s.ServeHTTP(w, r)
		}))
		defer h.Close()
		fmt.Printf("READY %s %s\n", h.URL, s.Info().EnvironmentID)
		scanner := bufio.NewScanner(os.Stdin)
		require.True(t, scanner.Scan())
		close(release)
		go func() { require.NoError(t, s.Clock(ctx, time.Millisecond)) }()
		for scanner.Scan() {
			if scanner.Text() == "new-fault" {
				require.NoError(t, sim.Advance(ctx, time.Minute))
				fmt.Println("CURRENT_FAULT new endpoint after rollback")
			}
		}
		return
	}
	db, e := storage.OpenSQLite(ctx, os.Getenv("TALON_PROCESS_DB"))
	require.NoError(t, e)
	defer db.Close()
	client, e := simulatorhttp.NewClient(os.Getenv("TALON_PROCESS_URL"), time.Minute)
	require.NoError(t, e)
	info, e := client.Info(ctx)
	require.NoError(t, e)
	cfg := ConnectedConfig{Config: Config{Storage: db, ScenarioID: info.IncidentID, Output: os.Stdout, RunConfig: runmeta.Config{EnvironmentID: info.EnvironmentID}, InvestigatorFactory: func(flow *workflow.IncidentWorkflow, p platform.ToolOpsPlatform) (controller.Investigator, error) {
		if mode == "agent" {
			return &intentInvestigator{flow: flow, incidentID: info.IncidentID}, nil
		}
		return &resumeInvestigator{flow: flow, call: func(ctx context.Context, instruction string) error {
			require.Contains(t, instruction, "必须读取当前环境的新证据")
			require.NotEmpty(t, flow.Snapshot().ActionResults)
			configs, e := p.GetConfigVersions(ctx, platform.StateQuery{Scope: platform.Scope{IncidentID: info.IncidentID}})
			require.NoError(t, e)
			healthy := false
			for _, c := range configs {
				if c.ID == "mapping-v1" && c.Active {
					healthy = true
				}
			}
			require.True(t, healthy, "fresh observation sees completed rollback")
			providers, e := p.GetProviders(ctx, platform.StateQuery{Scope: platform.Scope{IncidentID: info.IncidentID}})
			require.NoError(t, e)
			newFault := false
			for _, provider := range providers {
				if provider.Endpoint == "https://new-fault.invalid" {
					newFault = true
				}
			}
			require.True(t, newFault, "reinvestigation reads current new fault after original mapping was fixed")
			_, e = flow.Apply(workflow.Event{Type: workflow.EventEscalated, Actor: workflow.ActorAgent, Reason: "fresh current observation verified; human review"})
			return e
		}}, nil
	}}, Connector: client, ApprovalPollInterval: time.Millisecond}
	if mode == "agent" {
		_, e = RunConnected(ctx, cfg)
		require.NoError(t, e)
		return
	}
	deadline := time.NewTimer(40 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		out, e := Resume(ctx, ResumeConfig{ConnectedConfig: cfg, RunID: os.Getenv("TALON_PROCESS_RUN")})
		if e == nil && !out.Waiting {
			require.Equal(t, controller.StopEscalated, out.Controller.Reason)
			fmt.Println("RESUMED original history and current environment")
			return
		}
		if e != nil && !errors.Is(e, runlease.ErrBusy) {
			require.NoError(t, e)
		}
		select {
		case <-deadline.C:
			t.Fatal("resume ownership deadline")
		case <-tick.C:
		}
	}
}

func TestCrossProcessAcceptedOperationResume(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	start := func(mode string, extra ...string) (*exec.Cmd, <-chan string, io.WriteCloser) {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHTTPProcessHelper$", "-test.v")
		cmd.Env = append(os.Environ(), "TALON_PROCESS_HELPER="+mode)
		cmd.Env = append(cmd.Env, extra...)
		stdout, e := cmd.StdoutPipe()
		require.NoError(t, e)
		stdin, e := cmd.StdinPipe()
		require.NoError(t, e)
		cmd.Stderr = os.Stderr
		require.NoError(t, cmd.Start())
		t.Cleanup(func() { cmd.Process.Kill() })
		lines := make(chan string, 100)
		go func() {
			defer close(lines)
			scan := bufio.NewScanner(stdout)
			for scan.Scan() {
				lines <- scan.Text()
			}
		}()
		return cmd, lines, stdin
	}
	await := func(lines <-chan string, prefix string) string {
		for {
			select {
			case line, ok := <-lines:
				require.True(t, ok, "subprocess ended before %s", prefix)
				t.Log(line)
				if strings.HasPrefix(line, prefix) {
					return line
				}
			case <-ctx.Done():
				t.Fatal("subprocess synchronization timeout")
				return ""
			}
		}
	}
	server, serverLines, serverInput := start("server")
	ready := strings.Fields(await(serverLines, "READY "))
	url, environment := ready[1], ready[2]
	dbPath := "file:" + filepath.Join(t.TempDir(), "process.db") + "?_pragma=busy_timeout(5000)"
	db, e := storage.OpenSQLite(ctx, dbPath)
	require.NoError(t, e)
	defer db.Close()
	agent, agentLines, _ := start("agent", "TALON_PROCESS_DB="+dbPath, "TALON_PROCESS_URL="+url)
	runLine := await(agentLines, "[run]")
	runID := strings.TrimPrefix(strings.Fields(runLine)[1], "run_id=")
	require.Contains(t, runLine, environment)
	// External human decision; no automatic approval in the Agent path.
	require.Eventually(t, func() bool {
		pending, e := db.Approvals().ListPending(ctx)
		if e != nil || len(pending) == 0 {
			select {
			case line, ok := <-agentLines:
				if !ok {
					t.Fatal("Agent exited before approval")
				}
				t.Log("agent progress:", line)
			default:
			}
			if e != nil {
				t.Log("approval read:", e)
			}
			return false
		}
		for _, r := range pending {
			_, e = db.Approvals().Decide(ctx, approval.Decision{ID: r.ID, IntentID: r.IntentID, ActionID: r.ActionID, ActionDigest: r.ActionDigest, Status: approval.StatusApproved, DecidedBy: "acceptance-human", DecisionReason: "reviewed original plan"})
			if e != nil {
				return false
			}
		}
		return true
	}, 5*time.Second, time.Millisecond)
	acceptedLine := await(serverLines, "ACCEPTED ")
	var accepted struct {
		Result platform.Operation `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(acceptedLine, "ACCEPTED ")), &accepted))
	op := accepted.Result
	require.Equal(t, platform.OperationPending, op.Status)
	saved, e := db.Checkpoints().Get(ctx, runID)
	require.NoError(t, e)
	records, e := db.Executions().ListIntent(ctx, saved.Workflow.ExecutionIntent.ID)
	require.NoError(t, e)
	require.Len(t, records, 1)
	require.Empty(t, records[0].OperationID, "response barrier precedes ID persistence")
	require.NoError(t, agent.Process.Kill())
	require.Error(t, agent.Wait())
	_, e = fmt.Fprintln(serverInput, "advance")
	require.NoError(t, e)
	client, e := simulatorhttp.NewClient(url, time.Second)
	require.NoError(t, e)
	bound, e := client.ConnectRun(ctx, platform.RunConnection{EnvironmentID: environment, IncidentID: op.IncidentID, RunID: runID, Epoch: 1})
	require.NoError(t, e)
	require.Eventually(t, func() bool {
		found, e := bound.(platform.OperationKeyReader).GetOperationByKey(ctx, platform.OperationKeyQuery{IncidentID: op.IncidentID, IdempotencyKey: op.IdempotencyKey})
		return e == nil && found.ID == op.ID && found.Status == platform.OperationSucceeded
	}, 5*time.Second, time.Millisecond)
	_, e = fmt.Fprintln(serverInput, "new-fault")
	require.NoError(t, e)
	await(serverLines, "CURRENT_FAULT ")
	resumed, resumeLines, _ := start("resume", "TALON_PROCESS_DB="+dbPath, "TALON_PROCESS_URL="+url, "TALON_PROCESS_RUN="+runID)
	await(resumeLines, "RESUMED ")
	require.NoError(t, resumed.Wait())
	records, e = db.Executions().ListIntent(ctx, saved.Workflow.ExecutionIntent.ID)
	require.NoError(t, e)
	require.Equal(t, op.ID, records[0].OperationID)
	require.Equal(t, "succeeded", string(records[0].Status))
	final, e := db.Checkpoints().Get(ctx, runID)
	require.NoError(t, e)
	require.Equal(t, environment, final.RunConfig.EnvironmentID)
	require.Len(t, final.Workflow.ActionResults, 1)
	require.Greater(t, final.Revision, saved.Revision)
	// DryRun and original remediation only: no old probe/recovery or new repair replay.
	tasks, e := bound.GetTasks(ctx, platform.TaskQuery{Scope: platform.Scope{IncidentID: op.IncidentID}})
	require.NoError(t, e)
	require.Len(t, tasks, 2)
	require.NoError(t, server.Process.Kill())
	_ = server.Wait()
}
