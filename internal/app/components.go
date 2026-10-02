package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wen/opentalon/internal/agent"
	"github.com/wen/opentalon/internal/controller"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/runartifact"
	"github.com/wen/opentalon/internal/simulator"
	"github.com/wen/opentalon/internal/skill"
	"github.com/wen/opentalon/internal/workflow"
)

// buildInvestigator can be reused with an existing Workflow and platform; it does not start a run.
func buildInvestigator(ctx context.Context, cfg Config, prompts agent.PromptSet, flow *workflow.IncidentWorkflow, service *simulator.Simulator, recorder *runartifact.Recorder, printer *safePrinter) (controller.Investigator, error) {
	var investigator controller.Investigator
	var err error
	if cfg.InvestigatorFactory != nil {
		investigator, err = cfg.InvestigatorFactory(flow, service)
		if err != nil {
			return nil, fmt.Errorf("create scenario investigator: %w", err)
		}
	} else {
		if cfg.Model == nil {
			return nil, fmt.Errorf("model is required when investigator factory is not provided")
		}
		skillDirectory := strings.TrimSpace(cfg.SkillDirectory)
		if skillDirectory == "" {
			skillDirectory = skill.DefaultDirectory
		}
		registry, registryErr := skill.LoadDirectory(skillDirectory)
		if registryErr != nil {
			return nil, fmt.Errorf("load Skill Registry: %w", registryErr)
		}
		skillSession, sessionErr := skill.NewSession(registry, skill.DefaultMaxActive, recorder.ValidateEvidenceRefs)
		if sessionErr != nil {
			return nil, fmt.Errorf("create Skill session: %w", sessionErr)
		}
		printer.printf("[skill] catalog=%d active=0 max_active=%d\n", registry.Len(), skill.DefaultMaxActive)
		toolOpsAgent, buildErr := agent.NewToolOpsAgent(ctx, agent.Config{
			Model: cfg.Model, Platform: service, IncidentID: flow.Snapshot().IncidentID,
			VirtualTime: func() time.Time { return service.Snapshot().Now },
			Workflow:    flow, MaxSteps: cfg.AgentMaxSteps, MaxModelCalls: cfg.RunConfig.MaxModelCalls,
			Artifact: recorder, Skills: skillSession, Prompts: &prompts,
		})
		if buildErr != nil {
			return nil, fmt.Errorf("create ToolOps Agent: %w", buildErr)
		}
		investigator = &printingInvestigator{agent: toolOpsAgent, printer: printer}
	}
	if investigator.IncidentID() != flow.Snapshot().IncidentID {
		return nil, fmt.Errorf("investigator incident ID does not match scenario")
	}
	return investigator, nil
}

// assembleController wires synchronous persistence before returning runnable components.
func assembleController(cfg Config, service platform.ToolOpsPlatform, flow *workflow.IncidentWorkflow, investigator controller.Investigator, checkpoints *runCheckpointWriter, recorder *runartifact.Recorder) (*controller.IncidentController, *controller.ExecutionCoordinator, error) {
	artifactStore := cfg.Storage.RunArtifacts()
	// Audit surrounds the gate so failed intent saves appear in the investigation audit.
	investigator = &intentPersistenceGate{next: investigator, workflow: flow, checkpoints: checkpoints}
	investigator = &recordingInvestigator{next: investigator, workflow: flow, recorder: recorder, store: artifactStore}
	processor, err := controller.NewExecutionCoordinator(service, flow,
		controller.WithApprovalStore(cfg.Storage.Approvals()),
		controller.WithApprovalCheckpoint(checkpoints.saveAwaitingApproval),
		controller.WithExecutionStore(cfg.Storage.Executions(), checkpoints.metadata.RunID+"-scenario-worker", 5*time.Second),
		controller.WithAsyncExecution(controller.AsyncExecutionConfig{
			SubmitTimeout: 5 * time.Second, InitialPollInterval: 20 * time.Millisecond,
			MaxPollInterval: 100 * time.Millisecond, OperationTimeout: 2 * time.Minute,
		}),
		controller.WithWorkflowAudit(func(auditCtx context.Context, snapshot workflow.Snapshot) error {
			recorder.RecordWorkflow(snapshot)
			return artifactStore.Upsert(auditCtx, recorder.Snapshot())
		}),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create execution coordinator: %w", err)
	}
	orchestrator, err := controller.NewIncidentController(controller.IncidentControllerConfig{
		Workflow: flow, Investigator: investigator, ExecutionCoordinator: processor,
		WorkerRetryInterval: cfg.WorkerRetryInterval,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create incident controller: %w", err)
	}

	return orchestrator, processor, nil
}
