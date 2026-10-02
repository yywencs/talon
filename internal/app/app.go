// Package app 组装可执行的 Talon ToolOps 应用链路。
package app

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/wen/opentalon/internal/controller"
	"github.com/wen/opentalon/internal/observability"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/runartifact"
	"github.com/wen/opentalon/internal/runmeta"
	"github.com/wen/opentalon/internal/simulator"
	"github.com/wen/opentalon/internal/storage"
	"github.com/wen/opentalon/internal/workflow"
)

const defaultScenarioID = "mapping-regression-rollback-001"

// Config 定义一次独立、完整的 ToolOps 场景运行。
type Config struct {
	DatasetRoot         string
	SkillDirectory      string
	PromptDirectory     string
	ScenarioID          string
	Model               model.ToolCallingChatModel
	InvestigatorFactory func(*workflow.IncidentWorkflow, platform.ToolOpsPlatform) (controller.Investigator, error)
	Storage             *storage.Storage
	Output              io.Writer
	AutoApprove         bool
	AgentMaxSteps       int
	ClockPollInterval   time.Duration
	WorkerRetryInterval time.Duration
	Provenance          runmeta.Provenance
	RunConfig           runmeta.Config
}

// Result 汇总一次场景运行的最终状态与完整机器可读审计轨迹。
type Result struct {
	Controller controller.IncidentRunResult
	World      simulator.Snapshot
	Artifact   runartifact.RunArtifact
}

// Run prepares a new run, assembles its components, advances it and finalizes audit.
// Loading and restoring a saved Workflow is deliberately not implemented here.
func Run(ctx context.Context, cfg Config) (result Result, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	prepared, err := prepareRun(cfg)
	if err != nil {
		return Result{}, err
	}
	cfg = prepared.config
	cfg.RunConfig = prepared.runConfig
	recorder := runartifact.New(prepared.scenario.Metadata.ID, prepared.provenance, prepared.runConfig)
	metadata := recorder.Metadata()
	artifactStore := cfg.Storage.RunArtifacts()
	printer := &safePrinter{writer: cfg.Output}
	var flow *workflow.IncidentWorkflow
	var service *simulator.Simulator
	defer func() { err = finishRunAudit(ctx, artifactStore, recorder, flow, service, &result, err) }()
	if err := artifactStore.Upsert(ctx, recorder.Snapshot()); err != nil {
		return Result{}, fmt.Errorf("persist initial run artifact: %w", err)
	}
	flow, err = workflow.NewIncidentWorkflow(workflow.Config{IncidentID: prepared.scenario.Metadata.ID, IntentIDPrefix: metadata.RunID})
	if err != nil {
		return Result{}, fmt.Errorf("create incident workflow: %w", err)
	}
	checkpoints := &runCheckpointWriter{store: cfg.Storage.Checkpoints(), metadata: metadata, usage: recorder}
	if err := checkpoints.saveCreated(ctx, flow.Snapshot()); err != nil {
		return Result{}, err
	}
	printer.printf("[artifact] run_id=%s code_version=%s dataset_version=%s prompt_version=%s prompt_digest=%s schema=%s checkpoint=running\n",
		metadata.RunID, metadata.Provenance.CodeVersion, metadata.Provenance.DatasetVersion,
		metadata.Provenance.PromptVersion, metadata.Provenance.PromptDigest, runartifact.SchemaVersion)
	service, err = simulator.New(prepared.scenario)
	if err != nil {
		return Result{}, fmt.Errorf("create scenario simulator: %w", err)
	}
	incidentAt, err := agentStartOffset(prepared.scenario)
	if err != nil {
		return Result{}, err
	}
	if err := service.Advance(ctx, incidentAt); err != nil {
		return Result{}, fmt.Errorf("advance simulator to incident: %w", err)
	}
	printer.printf("[talon] scenario=%s title=%s\n", prepared.scenario.Metadata.ID, prepared.scenario.Metadata.Title)
	printer.printf("[simulator] advanced_to=%s incident_after=%s\n", service.Snapshot().Now.Format(time.RFC3339), incidentAt)
	ctx, finishTrace := observability.BeginCallback(ctx, "toolops.incident.run", map[string]any{
		"scenario_id": prepared.scenario.Metadata.ID, "title": prepared.scenario.Metadata.Title,
	})
	if traceID := observability.TraceIDFromContext(ctx); traceID != "" {
		printer.printf("[trace] trace_id=%s\n", traceID)
	}
	defer func() { finishTrace(result.Controller, err) }()
	investigator, err := buildInvestigator(ctx, cfg, prepared.prompts, flow, service, recorder, printer)
	if err != nil {
		return Result{}, err
	}
	orchestrator, processor, err := assembleController(cfg, service, flow, investigator, checkpoints, recorder)
	if err != nil {
		return Result{}, err
	}
	result, err = advanceRun(ctx, cfg, flow, service, orchestrator, processor, printer)
	if err == nil {
		printSummary(printer, result)
	}
	return result, err
}
