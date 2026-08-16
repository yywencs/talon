package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/wen/opentalon/internal/app"
	"github.com/wen/opentalon/internal/config"
	"github.com/wen/opentalon/internal/llm"
	"github.com/wen/opentalon/internal/observability"
	"github.com/wen/opentalon/internal/runartifact"
	"github.com/wen/opentalon/internal/storage"
)

type options struct {
	datasetRoot string
	scenarioID  string
	envFile     string
	timeout     time.Duration
	autoApprove bool
	maxSteps    int
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

	database, err := storage.OpenPostgresFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("open PostgreSQL runtime storage: %w", err)
	}
	defer database.Close()

	result, err := app.Run(ctx, app.Config{
		DatasetRoot: opts.datasetRoot, ScenarioID: opts.scenarioID,
		Model: chatModel, Storage: database, Output: os.Stdout,
		AutoApprove: opts.autoApprove, AgentMaxSteps: opts.maxSteps,
		Provenance: runartifact.Provenance{CodeVersion: strings.TrimSpace(os.Getenv("TALON_CODE_VERSION"))},
		RunConfig:  runartifact.RunConfig{ModelProvider: llmConfig.Provider, Model: llmConfig.Model},
	})
	if err != nil {
		return err
	}
	if result.Controller.Reason == "escalated" {
		return fmt.Errorf("incident was escalated; inspect the workflow output")
	}
	return nil
}

func parseOptions(arguments []string) (options, error) {
	set := flag.NewFlagSet("talon", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	var result options
	set.StringVar(&result.datasetRoot, "dataset", "data/toolops-v1", "ToolOps 数据集目录")
	set.StringVar(&result.scenarioID, "scenario", "mapping-regression-rollback-001", "要运行的场景 ID")
	set.StringVar(&result.envFile, "env-file", ".env", "存在时加载的 LLM 和 CozeLoop 环境变量文件；空值表示不加载")
	set.DurationVar(&result.timeout, "timeout", 5*time.Minute, "整个场景运行的真实时间上限")
	set.BoolVar(&result.autoApprove, "auto-approve", true, "仅在隔离的 Simulator 场景中自动批准待审批 Action")
	set.IntVar(&result.maxSteps, "max-agent-steps", 24, "单次 Agent ReAct 最大步骤数")
	if err := set.Parse(arguments); err != nil {
		return options{}, err
	}
	if set.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected positional arguments: %v", set.Args())
	}
	if result.timeout <= 0 {
		return options{}, fmt.Errorf("timeout must be positive")
	}
	if result.maxSteps <= 0 {
		return options{}, fmt.Errorf("max-agent-steps must be positive")
	}
	return result, nil
}
