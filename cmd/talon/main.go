package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/wen/opentalon/internal/app"
	"github.com/wen/opentalon/internal/buildinfo"
	"github.com/wen/opentalon/internal/config"
	"github.com/wen/opentalon/internal/experience"
	"github.com/wen/opentalon/internal/llm"
	"github.com/wen/opentalon/internal/observability"
	"github.com/wen/opentalon/internal/runmeta"
	"github.com/wen/opentalon/internal/scenario"
	"github.com/wen/opentalon/internal/simulatorhttp"
	"github.com/wen/opentalon/internal/storage"
)

type options struct {
	command, simulatorURL, runID, sqlite string
	httpTimeout, approvalPoll            time.Duration
	datasetRoot                          string
	scenarioID                           string
	envFile                              string
	timeout                              time.Duration
	autoApprove                          bool
	maxSteps                             int
	showVersion                          bool
	listScenarios                        bool
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintf(os.Stderr, "talon failed: %v\n", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	opts, err := parseOptions(arguments)
	if err != nil {
		return err
	}
	if opts.showVersion {
		fmt.Fprintf(os.Stdout, "%s (commit %s)\n", buildinfo.AgentVersion, buildinfo.Commit)
		return nil
	}
	if opts.listScenarios {
		dataset, err := scenario.LoadDataset(opts.datasetRoot)
		if err != nil {
			return fmt.Errorf("load scenarios: %w", err)
		}
		for _, item := range dataset.Cases {
			fmt.Fprintln(os.Stdout, item.Scenario.Metadata.ID)
		}
		return nil
	}
	if opts.envFile != "" {
		if err := godotenv.Load(opts.envFile); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("load env file %s: %w", opts.envFile, err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()

	observabilityConfig := observability.LoadConfigFromEnv()
	if err := observability.Init(ctx, observabilityConfig); err != nil {
		return fmt.Errorf("initialize observability: %w", err)
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := observability.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "warning: shutdown observability: %v\n", err)
		}
	}()

	llmConfig, err := config.LoadLLMConfig()
	if err != nil {
		return fmt.Errorf("load LLM config: %w", err)
	}
	chatModel, err := llm.NewChatModel(ctx, llmConfig)
	if err != nil {
		return fmt.Errorf("create LLM adapter: %w", err)
	}

	var database *storage.Storage
	if opts.sqlite != "" {
		database, err = storage.OpenSQLite(ctx, opts.sqlite)
	} else {
		database, err = storage.OpenPostgresFromEnv(ctx)
	}
	if err != nil {
		return fmt.Errorf("open PostgreSQL runtime storage: %w", err)
	}
	defer database.Close()

	cfg := app.Config{
		DatasetRoot: opts.datasetRoot, ScenarioID: opts.scenarioID,
		Model: chatModel, Storage: database, Output: os.Stdout,
		AutoApprove: opts.autoApprove, AgentMaxSteps: opts.maxSteps,
		PromptDirectory: llmConfig.PromptsDir,
		Provenance:      runmeta.Provenance{CodeVersion: buildinfo.AgentVersion},
		RunConfig:       runmeta.Config{ModelProvider: llmConfig.Provider, Model: llmConfig.Model},
	}
	if os.Getenv("EXPERIENCE_ENABLED") == "true" {
		cfg.Experiences, err = experience.ServiceFromEnv(database.Experiences())
		if err != nil {
			return err
		}
	}
	if opts.simulatorURL != "" {
		client, e := simulatorhttp.NewClient(opts.simulatorURL, opts.httpTimeout)
		if e != nil {
			return e
		}
		info, e := client.Info(ctx)
		if e != nil {
			return e
		}
		if info.DatasetVersion == "" {
			return fmt.Errorf("Simulator info missing dataset_version")
		}
		if opts.command != "resume" {
			cfg.Provenance.DatasetVersion = info.DatasetVersion
		}
		cfg.ScenarioID = info.IncidentID
		cfg.RunConfig.EnvironmentID = info.EnvironmentID
		connected := app.ConnectedConfig{Config: cfg, Connector: client, ApprovalPollInterval: opts.approvalPoll, VirtualTime: func() time.Time {
			current, e := client.Info(ctx)
			if e != nil {
				fmt.Fprintf(os.Stderr, "simulator clock unavailable: %v\n", e)
				cancel()
				return info.Now
			}
			return current.Now
		}}
		if opts.command == "resume" {
			out, e := app.Resume(ctx, app.ResumeConfig{ConnectedConfig: connected, RunID: opts.runID})
			fmt.Printf("[resume] run_id=%s environment_id=%s state=%s waiting=%t reason=%s final=%s\n", opts.runID, info.EnvironmentID, out.Controller.Snapshot.State, out.Waiting, out.Reason, out.Controller.Reason)
			return e
		}
		out, e := app.RunConnected(ctx, connected)
		fmt.Printf("[result] run_id=%s environment_id=%s state=%s final=%s\n", out.Snapshot.IntentIDPrefix, info.EnvironmentID, out.Snapshot.State, out.Reason)
		return e
	}
	_, err = app.Run(ctx, cfg)
	return err
}

func parseOptions(arguments []string) (options, error) {
	set := flag.NewFlagSet("talon", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	var result options
	if len(arguments) > 0 && (arguments[0] == "run" || arguments[0] == "resume") {
		result.command = arguments[0]
		arguments = arguments[1:]
	}
	set.StringVar(&result.simulatorURL, "simulator-url", "", "independent local Simulator URL")
	set.StringVar(&result.runID, "run-id", "", "original run ID for resume")
	set.StringVar(&result.sqlite, "sqlite-test-db", "", "SQLite file for local acceptance only; auto initializes schema")
	set.DurationVar(&result.httpTimeout, "http-timeout", 5*time.Second, "HTTP request timeout")
	set.DurationVar(&result.approvalPoll, "approval-poll", time.Second, "external human approval polling interval; zero returns at approval")
	set.StringVar(&result.datasetRoot, "dataset", "data/toolops-v1", "ToolOps 数据集目录")
	set.StringVar(&result.scenarioID, "scenario", "mapping-regression-rollback-001", "要运行的场景 ID")
	set.StringVar(&result.envFile, "env-file", ".env", "存在时加载的 LLM 和 CozeLoop 环境变量文件；空值表示不加载")
	set.DurationVar(&result.timeout, "timeout", 5*time.Minute, "整个场景运行的真实时间上限")
	set.BoolVar(&result.autoApprove, "auto-approve", true, "仅在隔离的 Simulator 场景中自动批准待审批 Action")
	set.IntVar(&result.maxSteps, "max-agent-steps", 24, "单次 Agent ReAct 最大步骤数")
	set.BoolVar(&result.showVersion, "version", false, "显示构建时注入的 Agent 版本和 Git commit")
	set.BoolVar(&result.listScenarios, "list-scenarios", false, "列出数据集中的场景 ID 后退出，不初始化模型或数据库")
	if err := set.Parse(arguments); err != nil {
		return options{}, err
	}
	if set.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected positional arguments: %v", set.Args())
	}
	if result.command == "resume" && (result.runID == "" || result.simulatorURL == "") {
		return options{}, fmt.Errorf("resume requires --run-id and --simulator-url")
	}
	if result.httpTimeout <= 0 || result.approvalPoll < 0 {
		return options{}, fmt.Errorf("invalid HTTP timeout or approval polling interval")
	}
	if result.timeout <= 0 {
		return options{}, fmt.Errorf("timeout must be positive")
	}
	if result.maxSteps <= 0 {
		return options{}, fmt.Errorf("max-agent-steps must be positive")
	}
	return result, nil
}
