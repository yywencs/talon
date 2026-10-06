// Package simulatorhttp implements the local-development Simulator protocol.
package simulatorhttp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/simulator"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Info struct {
	DatasetVersion string        `json:"dataset_version"`
	EnvironmentID  string        `json:"environment_id"`
	IncidentID     string        `json:"incident_id"`
	Now            time.Time     `json:"now"`
	EndAt          time.Time     `json:"end_at"`
	Tick           time.Duration `json:"tick_ns"`
	ClockRule      string        `json:"clock_rule"`
}
type request struct {
	Binding platform.RunConnection `json:"binding"`
	Payload json.RawMessage        `json:"payload"`
}
type wireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *wireError      `json:"error,omitempty"`
}
type accepted struct {
	fingerprint string
	id          string
	key         string
	err         error
}

// mu serializes activation and the entire synchronous Simulator acceptance call.
// The Simulator world mutex is nested inside mu. No path acquires them in reverse.
// Takeover waits for earlier accepted writes; it never cancels their scheduled work.
type Server struct {
	mu             sync.Mutex
	sim            *simulator.Simulator
	environment    string
	datasetVersion string
	epochs         map[string]uint64
	operations     map[string]accepted
	owners         map[string]string
}

func NewServer(sim *simulator.Simulator, datasetVersion string) (*Server, error) {
	if strings.TrimSpace(datasetVersion) == "" {
		return nil, fmt.Errorf("loaded dataset version required")
	}
	if sim == nil {
		return nil, fmt.Errorf("simulator required")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	return &Server{sim: sim, datasetVersion: strings.TrimSpace(datasetVersion), environment: hex.EncodeToString(id[:]), epochs: map[string]uint64{}, operations: map[string]accepted{}, owners: map[string]string{}}, nil
}
func (s *Server) Info() Info {
	v := s.sim.Snapshot()
	return Info{s.datasetVersion, s.environment, v.ScenarioID, v.Now, v.EndAt, v.Tick, "one virtual tick per wall interval while operations are pending/running; otherwise paused; capped at end_at"}
}

// Clock belongs to the service, not any request/Agent context.
func (s *Server) Clock(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("positive clock interval required")
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			v := s.sim.Snapshot()
			active := false
			for _, op := range v.Operations {
				if op.Status == platform.OperationPending || op.Status == platform.OperationRunning {
					active = true
				}
			}
			if !active || !v.Now.Before(v.EndAt) {
				continue
			}
			step := v.Tick
			if left := v.EndAt.Sub(v.Now); step > left {
				step = left
			}
			if err := s.sim.Advance(ctx, step); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	}
}
func decode(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return fmt.Errorf("%w: %v", platform.ErrPreconditionFailed, err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return platform.ErrPreconditionFailed
	}
	return nil
}
func (s *Server) check(b platform.RunConnection, write bool) error {
	if b.EnvironmentID != s.environment {
		return platform.ErrEnvironmentMismatch
	}
	if b.RunID == "" || b.Epoch == 0 {
		return platform.ErrUnauthorized
	}
	if b.IncidentID != s.Info().IncidentID {
		return platform.ErrNotFound
	}
	e := s.epochs[b.RunID]
	if e == 0 {
		return platform.ErrUnauthorized
	}
	if write && e != b.Epoch {
		return platform.ErrStaleEpoch
	}
	return nil
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" && r.Method == "GET" {
		w.WriteHeader(200)
		return
	}
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		s.respond(w, nil, platform.ErrPreconditionFailed)
		return
	}
	if r.URL.Path == "/v1/info" {
		s.respond(w, s.Info(), nil)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.Path == "/v1/connect" {
		var b platform.RunConnection
		err = decode(raw, &b)
		if err == nil {
			if b.EnvironmentID != s.environment {
				err = platform.ErrEnvironmentMismatch
			} else if b.RunID == "" || b.Epoch == 0 {
				err = platform.ErrUnauthorized
			} else if b.IncidentID != s.Info().IncidentID {
				err = platform.ErrNotFound
			} else if b.Epoch < s.epochs[b.RunID] {
				err = platform.ErrStaleEpoch
			} else {
				s.epochs[b.RunID] = b.Epoch
			}
		}
		s.respond(w, nil, err)
		return
	}
	var req request
	if err = decode(raw, &req); err != nil {
		s.respond(w, nil, err)
		return
	}
	write := false
	switch r.URL.Path {
	case "/v1/ExecuteRemediation", "/v1/RequestProbe", "/v1/RequestRecovery", "/v1/EscalateIncident":
		write = true
	}
	if err = s.check(req.Binding, write); err != nil {
		s.respond(w, nil, err)
		return
	}
	ctx := r.Context()
	var out any
	switch r.URL.Path {
	case "/v1/QueryMetrics":
		var q platform.MetricQuery
		err = decode(req.Payload, &q)
		if err != nil {
			break
		}
		out, err = s.sim.QueryMetrics(ctx, q)
	case "/v1/QueryLogs":
		var q platform.LogQuery
		err = decode(req.Payload, &q)
		if err != nil {
			break
		}
		out, err = s.sim.QueryLogs(ctx, q)
	case "/v1/QueryTraces":
		var q platform.TraceQuery
		err = decode(req.Payload, &q)
		if err != nil {
			break
		}
		out, err = s.sim.QueryTraces(ctx, q)
	case "/v1/GetServices":
		var q platform.StateQuery
		err = decode(req.Payload, &q)
		if err != nil {
			break
		}
		out, err = s.sim.GetServices(ctx, q)
	case "/v1/GetRoutes":
		var q platform.StateQuery
		err = decode(req.Payload, &q)
		if err != nil {
			break
		}
		out, err = s.sim.GetRoutes(ctx, q)
	case "/v1/GetProviders":
		var q platform.StateQuery
		err = decode(req.Payload, &q)
		if err != nil {
			break
		}
		out, err = s.sim.GetProviders(ctx, q)
	case "/v1/GetConfigVersions":
		var q platform.StateQuery
		err = decode(req.Payload, &q)
		if err != nil {
			break
		}
		out, err = s.sim.GetConfigVersions(ctx, q)
	case "/v1/GetChangeRecords":
		var q platform.ChangeQuery
		err = decode(req.Payload, &q)
		if err != nil {
			break
		}
		out, err = s.sim.GetChangeRecords(ctx, q)
	case "/v1/GetCredentialMetadata":
		var q platform.StateQuery
		err = decode(req.Payload, &q)
		if err != nil {
			break
		}
		out, err = s.sim.GetCredentialMetadata(ctx, q)
	case "/v1/GetConnectionMetadata":
		var q platform.StateQuery
		err = decode(req.Payload, &q)
		if err != nil {
			break
		}
		out, err = s.sim.GetConnectionMetadata(ctx, q)
	case "/v1/GetTasks":
		var q platform.TaskQuery
		err = decode(req.Payload, &q)
		if err != nil {
			break
		}
		out, err = s.sim.GetTasks(ctx, q)
	case "/v1/GetRemediationCapabilities":
		var q platform.StateQuery
		err = decode(req.Payload, &q)
		if err != nil {
			break
		}
		out, err = s.sim.GetRemediationCapabilities(ctx, q)
	case "/v1/GetRecoveryPolicies":
		var q platform.StateQuery
		err = decode(req.Payload, &q)
		if err != nil {
			break
		}
		out, err = s.sim.GetRecoveryPolicies(ctx, q)
	case "/v1/GetProbeEscalationPolicy":
		var q platform.StateQuery
		err = decode(req.Payload, &q)
		if err != nil {
			break
		}
		out, err = s.sim.GetProbeEscalationPolicy(ctx, q)
	case "/v1/ExecuteRemediation":
		var q platform.RemediationRequest
		err = decode(req.Payload, &q)
		if err != nil {
			break
		}
		if q.IncidentID != req.Binding.IncidentID {
			err = platform.ErrNotFound
			break
		}
		if q.IdempotencyKey == "" {
			err = platform.ErrPreconditionFailed
			break
		}
		original := q.IdempotencyKey
		canonical, _ := json.Marshal(q)
		fingerprint := "ExecuteRemediation:" + string(canonical)
		scope := req.Binding.RunID + ":" + original
		scopedBytes, _ := json.Marshal([]string{req.Binding.RunID, original})
		scope = string(scopedBytes)
		if a, ok := s.operations[scope]; ok {
			if a.fingerprint != fingerprint {
				err = platform.ErrConflict
				break
			}
			op, e := s.sim.GetOperation(ctx, platform.OperationQuery{IncidentID: q.IncidentID, OperationID: a.id})
			op.IdempotencyKey = original
			out = op
			err = e
			if err == nil {
				err = a.err
			}
			break
		}
		q.IdempotencyKey = scope
		op, e := s.sim.ExecuteRemediation(context.WithoutCancel(ctx), q)
		err = e
		if op.ID != "" {
			s.operations[scope] = accepted{fingerprint, op.ID, original, e}
			s.owners[op.ID] = req.Binding.RunID
			op.IdempotencyKey = original
		}
		out = op
	case "/v1/RequestProbe":
		var q platform.ProbeRequest
		err = decode(req.Payload, &q)
		if err != nil {
			break
		}
		if q.IncidentID != req.Binding.IncidentID {
			err = platform.ErrNotFound
			break
		}
		if q.IdempotencyKey == "" {
			err = platform.ErrPreconditionFailed
			break
		}
		original := q.IdempotencyKey
		canonical, _ := json.Marshal(q)
		fingerprint := "RequestProbe:" + string(canonical)
		scope := req.Binding.RunID + ":" + original
		scopedBytes, _ := json.Marshal([]string{req.Binding.RunID, original})
		scope = string(scopedBytes)
		if a, ok := s.operations[scope]; ok {
			if a.fingerprint != fingerprint {
				err = platform.ErrConflict
				break
			}
			op, e := s.sim.GetOperation(ctx, platform.OperationQuery{IncidentID: q.IncidentID, OperationID: a.id})
			op.IdempotencyKey = original
			out = op
			err = e
			if err == nil {
				err = a.err
			}
			break
		}
		q.IdempotencyKey = scope
		op, e := s.sim.RequestProbe(context.WithoutCancel(ctx), q)
		err = e
		if op.ID != "" {
			s.operations[scope] = accepted{fingerprint, op.ID, original, e}
			s.owners[op.ID] = req.Binding.RunID
			op.IdempotencyKey = original
		}
		out = op
	case "/v1/RequestRecovery":
		var q platform.RecoveryRequest
		err = decode(req.Payload, &q)
		if err != nil {
			break
		}
		if q.IncidentID != req.Binding.IncidentID {
			err = platform.ErrNotFound
			break
		}
		if q.IdempotencyKey == "" {
			err = platform.ErrPreconditionFailed
			break
		}
		original := q.IdempotencyKey
		canonical, _ := json.Marshal(q)
		fingerprint := "RequestRecovery:" + string(canonical)
		scope := req.Binding.RunID + ":" + original
		scopedBytes, _ := json.Marshal([]string{req.Binding.RunID, original})
		scope = string(scopedBytes)
		if a, ok := s.operations[scope]; ok {
			if a.fingerprint != fingerprint {
				err = platform.ErrConflict
				break
			}
			op, e := s.sim.GetOperation(ctx, platform.OperationQuery{IncidentID: q.IncidentID, OperationID: a.id})
			op.IdempotencyKey = original
			out = op
			err = e
			if err == nil {
				err = a.err
			}
			break
		}
		q.IdempotencyKey = scope
		op, e := s.sim.RequestRecovery(context.WithoutCancel(ctx), q)
		err = e
		if op.ID != "" {
			s.operations[scope] = accepted{fingerprint, op.ID, original, e}
			s.owners[op.ID] = req.Binding.RunID
			op.IdempotencyKey = original
		}
		out = op
	case "/v1/GetOperation":
		var q platform.OperationQuery
		err = decode(req.Payload, &q)
		if err != nil {
			break
		}
		if s.owners[q.OperationID] != req.Binding.RunID {
			err = platform.ErrNotFound
			break
		}
		op, e := s.sim.GetOperation(ctx, q)
		err = e
		for _, a := range s.operations {
			if a.id == op.ID {
				op.IdempotencyKey = a.key
				break
			}
		}
		out = op
	case "/v1/EscalateIncident":
		var q platform.EscalationRequest
		err = decode(req.Payload, &q)
		if err != nil {
			break
		}
		if q.IncidentID != req.Binding.IncidentID {
			err = platform.ErrNotFound
			break
		}
		if q.IdempotencyKey == "" {
			err = platform.ErrPreconditionFailed
			break
		}
		original := q.IdempotencyKey
		canonical, _ := json.Marshal(q)
		fingerprint := "EscalateIncident:" + string(canonical)
		scope := req.Binding.RunID + ":" + original
		scopedBytes, _ := json.Marshal([]string{req.Binding.RunID, original})
		scope = string(scopedBytes)
		if a, ok := s.operations[scope]; ok {
			if a.fingerprint != fingerprint {
				err = platform.ErrConflict
				break
			}
			op, e := s.sim.GetOperation(ctx, platform.OperationQuery{IncidentID: q.IncidentID, OperationID: a.id})
			op.IdempotencyKey = original
			out = op
			err = e
			if err == nil {
				err = a.err
			}
			break
		}
		q.IdempotencyKey = scope
		op, e := s.sim.EscalateIncident(context.WithoutCancel(ctx), q)
		err = e
		if op.ID != "" {
			s.operations[scope] = accepted{fingerprint, op.ID, original, e}
			s.owners[op.ID] = req.Binding.RunID
			op.IdempotencyKey = original
		}
		out = op
	case "/v1/GetOperationByKey":
		var q platform.OperationKeyQuery
		err = decode(req.Payload, &q)
		if err != nil {
			break
		}
		if q.IncidentID != req.Binding.IncidentID {
			err = platform.ErrNotFound
			break
		}
		b, _ := json.Marshal([]string{req.Binding.RunID, q.IdempotencyKey})
		a, ok := s.operations[string(b)]
		if !ok {
			err = platform.ErrNotFound
			break
		}
		op, e := s.sim.GetOperation(ctx, platform.OperationQuery{IncidentID: q.IncidentID, OperationID: a.id})
		op.IdempotencyKey = a.key
		out = op
		err = e
	default:
		err = platform.ErrUnsupported
	}
	s.respond(w, out, err)
}

var codes = map[string]error{"not_found": platform.ErrNotFound, "unauthorized": platform.ErrUnauthorized, "conflict": platform.ErrConflict, "precondition_failed": platform.ErrPreconditionFailed, "unsupported": platform.ErrUnsupported, "stale_epoch": platform.ErrStaleEpoch, "environment_mismatch": platform.ErrEnvironmentMismatch}

func errorFromCode(code string) error {
	if e := codes[code]; e != nil {
		return e
	}
	return platform.ErrOutcomeUnknown
}
func (s *Server) respond(w http.ResponseWriter, out any, err error) {
	wire := response{}
	status := 200
	if out != nil {
		wire.Result, _ = json.Marshal(out)
	}
	if err != nil {
		code := "internal"
		status = 500
		for k, e := range codes {
			if errors.Is(err, e) {
				code = k
				status = 400
				switch k {
				case "not_found":
					status = 404
				case "unauthorized":
					status = 403
				case "conflict", "stale_epoch", "environment_mismatch":
					status = 409
				}
				break
			}
		}
		wire.Error = &wireError{code, err.Error()}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(wire)
}
