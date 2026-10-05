package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/wen/opentalon/internal/agent"
	"github.com/wen/opentalon/internal/controller"
	"github.com/wen/opentalon/internal/execution"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/runartifact"
	"github.com/wen/opentalon/internal/workflow"
)

// ConnectedConfig uses an independently running environment. The connector must
// verify the saved environment identity and enforce fencing at its write boundary.
// Talon does not create/reset the Simulator or own its clock through this API.
type ConnectedConfig struct {
	Config
	Connector   platform.RunConnector
	VirtualTime func() time.Time
	// Zero returns at approval; positive duration waits for external human decisions.
	ApprovalPollInterval time.Duration
}
type ResumeConfig struct {
	ConnectedConfig
	RunID string
}
type ResumeResult struct {
	Controller     controller.IncidentRunResult
	Reconciliation *controller.Reconciliation
	Waiting        bool
	Reason         string
}

// Resume acquires ownership, reconciles ALL old operations, persists a fresh
// investigation boundary and runs the existing controller on the original run.
// Waiting is a safe stop, not permission to execute the old plan. Call Resume
// again later; this API deliberately does not install a background poller.
func Resume(ctx context.Context, cfg ResumeConfig) (out ResumeResult, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg.Storage == nil || cfg.Connector == nil || cfg.RunID == "" {
		return out, fmt.Errorf("storage, run ID and fenced platform connector required")
	}
	session, err := acquireRun(ctx, cfg.Storage, cfg.RunID)
	if err != nil {
		return out, err
	}
	defer session.close()
	ctx = session.ctx
	cfg.Storage = cfg.Storage.OwnedRun(session.token)
	loaded, err := LoadWorkflow(ctx, cfg.Storage.Checkpoints(), cfg.RunID)
	if err != nil {
		return out, err
	}
	flow := loaded.Workflow
	saved := loaded.Checkpoint
	out.Controller.Snapshot = flow.Snapshot()
	if reason, terminal := terminalReason(flow.Snapshot().State); terminal {
		out.Controller.Reason = reason
		return out, nil
	}
	if saved.RunConfig.EnvironmentID == "" {
		return out, fmt.Errorf("checkpoint has no stable external environment identity; cannot safely reconnect")
	}
	service, err := cfg.Connector.ConnectRun(ctx, platform.RunConnection{EnvironmentID: saved.RunConfig.EnvironmentID,
		IncidentID: saved.Workflow.IncidentID, RunID: saved.RunID, Epoch: session.token.Epoch})
	if err != nil {
		return out, err
	}
	if service == nil {
		return out, fmt.Errorf("connector returned no platform")
	}
	reconcileCfg := controller.ReconcileConfig{Approvals: cfg.Storage.Approvals(), Executions: cfg.Storage.Executions(), Operations: service}
	// Each successful pass commits one terminal operation; the finite action
	// budget bounds this loop. No old action can be submitted by this path.
	for pass := 0; pass <= saved.Workflow.ActionsAccepted; pass++ {
		r, reconcileErr := controller.ReconcileForResume(ctx, saved.Workflow, reconcileCfg)
		if reconcileErr != nil {
			return out, reconcileErr
		}
		out.Reconciliation = r
		if r.Next != controller.ReconcileCommitExecution {
			break
		}
		op := r.Operations[len(r.Operations)-1]
		var observed execution.Record
		for _, record := range r.Executions {
			if record.OperationID == op.ID {
				observed = record
				break
			}
		}
		if err := cfg.Storage.CommitObservedExecution(ctx, session.token, observed, op); err != nil {
			if errors.Is(err, execution.ErrNoClaimable) {
				out.Waiting = true
				out.Reason = "action observation changed or its lease is still active; retry reconciliation later"
				return out, nil
			}
			return out, err
		}
	}
	r := out.Reconciliation
	if r.Next != controller.ReconcileInvestigate {
		out.Waiting = true
		out.Reason = r.Reason
		return out, nil
	}
	flow, err = workflow.Restore(r.Workflow.Snapshot())
	if err != nil {
		return out, err
	}
	if err := flow.ResumeInvestigation(); err != nil {
		return out, err
	}
	artifact, err := cfg.Storage.RunArtifacts().Get(ctx, saved.RunID)
	if err != nil {
		return out, fmt.Errorf("load original run audit: %w", err)
	}
	if artifact.ScenarioID != saved.Workflow.IncidentID || artifact.RunID != saved.RunID || artifact.Provenance != saved.Provenance || artifact.RunConfig != saved.RunConfig {
		return out, fmt.Errorf("audit identity or immutable run inputs disagree with checkpoint")
	}
	recorder, err := runartifact.Resume(artifact, saved.ModelCallsUsed)
	if err != nil {
		return out, err
	}
	cfg.RunConfig = saved.RunConfig
	cfg.Provenance = saved.Provenance
	cfg.AgentMaxSteps = saved.RunConfig.AgentMaxSteps
	checkpoints := &runCheckpointWriter{trackProgress: true, store: cfg.Storage.Checkpoints(), metadata: recorder.Metadata(), usage: recorder, revision: saved.Revision}
	if saved.Workflow.ExecutionIntent != nil {
		checkpoints.lastIntentID = saved.Workflow.ExecutionIntent.ID
	}
	if err := checkpoints.saveProgress(ctx, flow.Snapshot()); err != nil {
		return out, err
	}
	out.Controller, err = runConnectedController(ctx, cfg.ConnectedConfig, service, flow, recorder, checkpoints)
	return out, err
}

// RunConnected starts a new resumable run against an existing environment. The
// caller supplies its stable EnvironmentID in RunConfig and incident in ScenarioID.
func RunConnected(ctx context.Context, cfg ConnectedConfig) (controller.IncidentRunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg.Storage == nil || cfg.Connector == nil || cfg.RunConfig.EnvironmentID == "" || cfg.ScenarioID == "" {
		return controller.IncidentRunResult{}, fmt.Errorf("storage, connector, incident and environment identity required")
	}
	cfg.RunConfig.AgentMaxSteps = cfg.AgentMaxSteps
	if cfg.RunConfig.AgentMaxSteps == 0 {
		cfg.RunConfig.AgentMaxSteps = agent.DefaultMaxSteps
	}
	cfg.AgentMaxSteps = cfg.RunConfig.AgentMaxSteps
	if cfg.RunConfig.MaxModelCalls == 0 {
		cfg.RunConfig.MaxModelCalls = agent.DefaultMaxModelCalls
	}
	prompts, err := agent.LoadPromptSet(cfg.PromptDirectory)
	if err != nil {
		return controller.IncidentRunResult{}, err
	}
	if cfg.Provenance.PromptDigest != "" && cfg.Provenance.PromptDigest != prompts.Digest {
		return controller.IncidentRunResult{}, fmt.Errorf("configured prompt digest mismatch")
	}
	cfg.Provenance.PromptDigest = prompts.Digest
	cfg.Provenance.PromptVersion = prompts.Version
	recorder := runartifact.New(cfg.ScenarioID, cfg.Provenance, cfg.RunConfig)
	cfg.RunConfig = recorder.Metadata().Config
	session, err := acquireRun(ctx, cfg.Storage, recorder.Metadata().RunID)
	if err != nil {
		return controller.IncidentRunResult{}, err
	}
	defer session.close()
	ctx = session.ctx
	cfg.Storage = cfg.Storage.OwnedRun(session.token)
	flow, err := workflow.NewIncidentWorkflow(workflow.Config{IncidentID: cfg.ScenarioID, IntentIDPrefix: recorder.Metadata().RunID})
	if err != nil {
		return controller.IncidentRunResult{}, err
	}
	if err := cfg.Storage.RunArtifacts().Upsert(ctx, recorder.Snapshot()); err != nil {
		return controller.IncidentRunResult{}, err
	}
	checkpoints := &runCheckpointWriter{trackProgress: true, store: cfg.Storage.Checkpoints(), metadata: recorder.Metadata(), usage: recorder}
	if err := checkpoints.saveCreated(ctx, flow.Snapshot()); err != nil {
		return controller.IncidentRunResult{}, err
	}
	service, err := cfg.Connector.ConnectRun(ctx, platform.RunConnection{EnvironmentID: cfg.RunConfig.EnvironmentID, IncidentID: cfg.ScenarioID, RunID: session.token.RunID, Epoch: session.token.Epoch})
	if err != nil {
		return controller.IncidentRunResult{}, err
	}
	if service == nil {
		return controller.IncidentRunResult{}, fmt.Errorf("connector returned no platform")
	}
	return runConnectedController(ctx, cfg, service, flow, recorder, checkpoints)
}

func runConnectedController(ctx context.Context, cfg ConnectedConfig, service platform.ToolOpsPlatform, flow *workflow.IncidentWorkflow, recorder *runartifact.Recorder, checkpoints *runCheckpointWriter) (result controller.IncidentRunResult, err error) {
	if cfg.Output == nil {
		cfg.Output = io.Discard
	}
	defer func() {
		result.Snapshot = flow.Snapshot()
		// Even on cancellation, preserve accepted state if this process still owns
		// the run. Both checkpoint and audit writes are fenced in their transactions.
		saveErr := checkpoints.saveProgress(ctx, flow.Snapshot())
		recorder.RecordWorkflow(flow.Snapshot())
		artifact := recorder.Snapshot()
		if _, terminal := terminalReason(flow.Snapshot().State); terminal {
			artifact = recorder.Finish(string(result.Reason), flow.Snapshot(), err)
		}
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err = errors.Join(err, saveErr, cfg.Storage.RunArtifacts().Upsert(persistCtx, artifact))
	}()
	if reason, terminal := terminalReason(flow.Snapshot().State); terminal {
		result.Reason = reason
		return result, nil
	}
	prompts, err := agent.LoadPromptSet(cfg.PromptDirectory)
	if err != nil {
		return result, err
	}
	if cfg.Provenance.PromptDigest != "" && cfg.Provenance.PromptDigest != prompts.Digest {
		return result, fmt.Errorf("prompt digest differs from saved run")
	}
	investigator, err := buildPlatformInvestigator(ctx, cfg.Config, prompts, flow, service, cfg.VirtualTime, recorder, &safePrinter{writer: cfg.Output})
	if err != nil {
		return result, err
	}
	orchestrator, processor, err := assembleController(cfg.Config, service, flow, investigator, checkpoints, recorder)
	if err != nil {
		return result, err
	}
	// External environments always stop at human approval; isolated runner's
	// AutoApprove flag cannot authorize newly investigated production actions.
	for {
		result, err = orchestrator.Run(ctx)
		if err != nil || result.Reason != controller.StopAwaitingApproval || cfg.ApprovalPollInterval <= 0 {
			return result, err
		}
		for flow.Snapshot().State == workflow.StateAwaitingApproval {
			timer := time.NewTimer(cfg.ApprovalPollInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return result, context.Cause(ctx)
			case <-timer.C:
			}
			if err := processor.RefreshApprovals(ctx); err != nil {
				return result, err
			}
		}
	}
}
func terminalReason(state workflow.State) (controller.StopReason, bool) {
	switch state {
	case workflow.StateResolved:
		return controller.StopResolved, true
	case workflow.StateFailed:
		return controller.StopFailed, true
	case workflow.StateBlocked:
		return controller.StopBlocked, true
	case workflow.StateEscalated:
		return controller.StopEscalated, true
	}
	return "", false
}
