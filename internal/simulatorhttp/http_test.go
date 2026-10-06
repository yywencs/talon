package simulatorhttp

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/scenario"
	"github.com/wen/opentalon/internal/simulator"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Server, *Client, *simulator.Simulator) {
	t.Helper()
	ds, e := scenario.LoadDataset("../../data/toolops-v1")
	require.NoError(t, e)
	item, ok := ds.Find("mapping-regression-rollback-001")
	require.True(t, ok)
	sim, e := simulator.New(item.Scenario)
	require.NoError(t, e)
	require.NoError(t, sim.Advance(context.Background(), 5*time.Minute))
	s, e := NewServer(sim, ds.Version)
	require.NoError(t, e)
	h := httptest.NewServer(s)
	t.Cleanup(h.Close)
	c, e := NewClient(h.URL, time.Second)
	require.NoError(t, e)
	return s, c, sim
}
func bind(t *testing.T, c *Client, s *Server, run string, epoch uint64) *Client {
	t.Helper()
	p, e := c.ConnectRun(context.Background(), platform.RunConnection{EnvironmentID: s.Info().EnvironmentID, IncidentID: s.Info().IncidentID, RunID: run, Epoch: epoch})
	require.NoError(t, e)
	return p.(*Client)
}
func rollback(s *Server, key string) platform.RemediationRequest {
	return platform.RemediationRequest{IncidentID: s.Info().IncidentID, ToolName: "rollback_mapping", Arguments: map[string]any{"tool_id": "generate_image", "target_version": "mapping-v1", "large": json.Number("9007199254740993")}, ExpectedVersion: "mapping-v2", IdempotencyKey: key}
}
func TestHTTPRoundTripIdempotencyAndFullFlow(t *testing.T) {
	s, c, sim := fixture(t)
	ctx := context.Background()
	c = bind(t, c, s, "r", 1)
	q := platform.StateQuery{Scope: platform.Scope{IncidentID: s.Info().IncidentID}}
	a, e := c.GetRoutes(ctx, q)
	require.NoError(t, e)
	b, e := sim.GetRoutes(ctx, q)
	require.NoError(t, e)
	require.Equal(t, b, a)
	p, e := c.GetRecoveryPolicies(ctx, q)
	require.NoError(t, e)
	require.Equal(t, "default-safe-recovery", p[0].ID)
	req := rollback(s, "k")
	op, e := c.ExecuteRemediation(ctx, req)
	require.NoError(t, e)
	again, e := c.ExecuteRemediation(ctx, req)
	require.NoError(t, e)
	require.Equal(t, op.ID, again.ID)
	require.Len(t, sim.Snapshot().Operations, 1)
	// The fingerprint contains the exact integer decoded by the HTTP server.
	s.mu.Lock()
	for _, a := range s.operations {
		require.Contains(t, a.fingerprint, "9007199254740993")
	}
	s.mu.Unlock()
	req.Arguments["large"] = json.Number("9007199254740992")
	_, e = c.ExecuteRemediation(ctx, req)
	require.ErrorIs(t, e, platform.ErrConflict)
	req = rollback(s, "k")
	req.DryRun = true
	_, e = c.ExecuteRemediation(ctx, req)
	require.ErrorIs(t, e, platform.ErrConflict)
	require.NoError(t, sim.Advance(ctx, time.Minute))
	op, e = c.GetOperationByKey(ctx, platform.OperationKeyQuery{IncidentID: req.IncidentID, IdempotencyKey: "k"})
	require.NoError(t, e)
	require.Equal(t, platform.OperationSucceeded, op.Status)
	require.Equal(t, "k", op.IdempotencyKey)
	probe, e := c.RequestProbe(ctx, platform.ProbeRequest{IncidentID: req.IncidentID, RouteID: "route-a", PolicyID: p[0].ID, IdempotencyKey: "probe"})
	require.NoError(t, e)
	require.NoError(t, sim.Advance(ctx, 6*time.Minute))
	probe, e = c.GetOperation(ctx, platform.OperationQuery{IncidentID: req.IncidentID, OperationID: probe.ID})
	require.NoError(t, e)
	require.Equal(t, platform.OperationSucceeded, probe.Status)
	recovery, e := c.RequestRecovery(ctx, platform.RecoveryRequest{IncidentID: req.IncidentID, RouteID: "route-a", PolicyID: p[0].ID, IdempotencyKey: "recover"})
	require.NoError(t, e)
	require.NoError(t, sim.Advance(ctx, 12*time.Minute))
	recovery, e = c.GetOperation(ctx, platform.OperationQuery{IncidentID: req.IncidentID, OperationID: recovery.ID})
	require.NoError(t, e)
	require.Equal(t, platform.OperationSucceeded, recovery.Status)
}
func TestFencingIdentityAndTaskIsolation(t *testing.T) {
	s, base, sim := fixture(t)
	ctx := context.Background()
	old := bind(t, base, s, "r", 1)
	bind(t, base, s, "r", 1)
	other := bind(t, base, s, "other", 1)
	op, e := old.ExecuteRemediation(ctx, rollback(s, "same"))
	require.NoError(t, e)
	_, e = other.GetOperationByKey(ctx, platform.OperationKeyQuery{IncidentID: op.IncidentID, IdempotencyKey: "same"})
	require.ErrorIs(t, e, platform.ErrNotFound)
	_, e = other.GetOperation(ctx, platform.OperationQuery{IncidentID: op.IncidentID, OperationID: op.ID})
	require.ErrorIs(t, e, platform.ErrNotFound)
	bind(t, base, s, "r", 2)
	r := rollback(s, "dry")
	r.DryRun = true
	_, e = old.ExecuteRemediation(ctx, r)
	require.ErrorIs(t, e, platform.ErrStaleEpoch)
	_, e = old.RequestProbe(ctx, platform.ProbeRequest{})
	require.ErrorIs(t, e, platform.ErrStaleEpoch)
	_, e = old.RequestRecovery(ctx, platform.RecoveryRequest{})
	require.ErrorIs(t, e, platform.ErrStaleEpoch)
	_, e = old.EscalateIncident(ctx, platform.EscalationRequest{})
	require.ErrorIs(t, e, platform.ErrStaleEpoch)
	_, e = base.ExecuteRemediation(ctx, r)
	require.Error(t, e)
	require.Len(t, sim.Snapshot().Operations, 1)
	_, e = base.ConnectRun(ctx, platform.RunConnection{EnvironmentID: "wrong", IncidentID: op.IncidentID, RunID: "r", Epoch: 3})
	require.ErrorIs(t, e, platform.ErrEnvironmentMismatch)
	s2, c2, _ := fixture(t)
	require.NotEqual(t, s.environment, s2.environment)
	_, e = c2.ConnectRun(ctx, old.binding)
	require.ErrorIs(t, e, platform.ErrEnvironmentMismatch)
}
func TestConcurrentTakeoverAndWrite(t *testing.T) {
	s, c, sim := fixture(t)
	old := bind(t, c, s, "r", 1)
	ctx := context.Background()
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, e := old.ExecuteRemediation(ctx, rollback(s, "same"))
			if e != nil && !errors.Is(e, platform.ErrStaleEpoch) {
				t.Errorf("write: %v", e)
			}
		}()
	}
	close(start)
	newClient := bind(t, c, s, "r", 2)
	wg.Wait()
	_, e := old.ExecuteRemediation(ctx, rollback(s, "after"))
	require.ErrorIs(t, e, platform.ErrStaleEpoch)
	require.LessOrEqual(t, len(sim.Snapshot().Operations), 1)
	_, e = newClient.ExecuteRemediation(ctx, rollback(s, "same"))
	require.NoError(t, e)
	require.Len(t, sim.Snapshot().Operations, 1)
}
func TestAcceptedResponseLostAndClockIndependent(t *testing.T) {
	s, base, sim := fixture(t)
	base = bind(t, base, s, "r", 1)
	accepted := make(chan struct{})
	release := make(chan struct{})
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/ExecuteRemediation" {
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, r)
			close(accepted)
			<-release
			conn, _, e := w.(http.Hijacker).Hijack()
			if e == nil {
				conn.Close()
			}
			return
		}
		s.ServeHTTP(w, r)
	}))
	defer h.Close()
	c, e := NewClient(h.URL, time.Second)
	require.NoError(t, e)
	c.binding = base.binding
	done := make(chan error, 1)
	go func() { _, e := c.ExecuteRemediation(context.Background(), rollback(s, "lost")); done <- e }()
	<-accepted
	found, e := base.GetOperationByKey(context.Background(), platform.OperationKeyQuery{IncidentID: s.Info().IncidentID, IdempotencyKey: "lost"})
	require.NoError(t, e)
	require.Equal(t, platform.OperationPending, found.Status)
	close(release)
	require.ErrorIs(t, <-done, platform.ErrOutcomeUnknown)
	ctx, cancel := context.WithCancel(context.Background())
	clockDone := make(chan error, 1)
	go func() { clockDone <- s.Clock(ctx, time.Millisecond) }()
	require.Eventually(t, func() bool { return sim.Snapshot().Operations[found.ID].Status == platform.OperationSucceeded }, time.Second, time.Millisecond)
	cancel()
	require.NoError(t, <-clockDone)
	again, e := base.GetOperationByKey(context.Background(), platform.OperationKeyQuery{IncidentID: found.IncidentID, IdempotencyKey: "lost"})
	require.NoError(t, e)
	require.Equal(t, found.ID, again.ID)
	require.Len(t, sim.Snapshot().Operations, 1)
}

func TestEscalationLargeIntegerResponseAndExplicitRejection(t *testing.T) {
	s, c, _ := fixture(t)
	c = bind(t, c, s, "r", 1)
	ctx := context.Background()
	op, e := c.EscalateIncident(ctx, platform.EscalationRequest{IncidentID: s.Info().IncidentID, IdempotencyKey: "large", ReasonCode: platform.EscalationReasonNoSafeRemediationAvailable, Reason: "manual review", Handoff: platform.EscalationHandoff{AffectedService: "service", CurrentProtectionState: map[string]any{"generation": json.Number("9223372036854775807")}, RecommendedHumanAction: "inspect"}})
	require.NoError(t, e)
	handoff := op.Result["handoff"].(map[string]any)
	state := handoff["current_protection_state"].(map[string]any)
	require.Equal(t, json.Number("9223372036854775807"), state["generation"])
	req := rollback(s, "rejected")
	req.ExpectedVersion = "wrong"
	op, e = c.ExecuteRemediation(ctx, req)
	require.ErrorIs(t, e, platform.ErrConflict)
	require.NotErrorIs(t, e, platform.ErrOutcomeUnknown)
	require.Equal(t, platform.OperationRejected, op.Status)
	found, e := c.GetOperationByKey(ctx, platform.OperationKeyQuery{IncidentID: req.IncidentID, IdempotencyKey: req.IdempotencyKey})
	require.NoError(t, e)
	require.Equal(t, op.ID, found.ID)
}
func TestContextCancellationDoesNotRetryWrites(t *testing.T) {
	s, c, sim := fixture(t)
	c = bind(t, c, s, "r", 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, e := c.ExecuteRemediation(ctx, rollback(s, "cancelled"))
	require.ErrorIs(t, e, context.Canceled)
	require.ErrorIs(t, e, platform.ErrOutcomeUnknown)
	require.Empty(t, sim.Snapshot().Operations)
}

type acceptanceWriter struct {
	*httptest.ResponseRecorder
	accepted chan struct{}
	release  chan struct{}
}

func (w *acceptanceWriter) WriteHeader(code int) {
	close(w.accepted)
	<-w.release
	w.ResponseRecorder.WriteHeader(code)
}
func TestActivationSharesAcceptanceMutex(t *testing.T) {
	s, c, sim := fixture(t)
	old := bind(t, c, s, "r", 1)
	req := rollback(s, "boundary")
	payload, e := json.Marshal(request{Binding: old.binding, Payload: mustTestJSON(t, req)})
	require.NoError(t, e)
	w := &acceptanceWriter{httptest.NewRecorder(), make(chan struct{}), make(chan struct{})}
	r := httptest.NewRequest("POST", "/v1/ExecuteRemediation", strings.NewReader(string(payload)))
	finished := make(chan struct{})
	go func() { s.ServeHTTP(w, r); close(finished) }()
	<-w.accepted
	require.Len(t, sim.Snapshot().Operations, 1, "Simulator accepted the business operation")
	if s.mu.TryLock() {
		s.mu.Unlock()
		t.Fatal("fencing lock released before synchronous acceptance returned")
	}
	close(w.release)
	<-finished
	bind(t, c, s, "r", 2)
	_, e = old.ExecuteRemediation(context.Background(), rollback(s, "late"))
	require.ErrorIs(t, e, platform.ErrStaleEpoch)
	require.Len(t, sim.Snapshot().Operations, 1)
}
func mustTestJSON(t *testing.T, v any) json.RawMessage {
	b, e := json.Marshal(v)
	require.NoError(t, e)
	return b
}

func TestDatasetMetadataMissingIsRejected(t *testing.T) {
	s, _, sim := fixture(t)
	_, e := NewServer(sim, "")
	require.Error(t, e)
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(response{Result: mustTestJSON(t, Info{EnvironmentID: s.environment, IncidentID: s.Info().IncidentID})})
	}))
	defer h.Close()
	c, e := NewClient(h.URL, time.Second)
	require.NoError(t, e)
	_, e = c.EnvironmentMetadata(context.Background())
	require.ErrorContains(t, e, "missing dataset_version")
}
