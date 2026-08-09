// benchmark provides offline, reproducible evaluation workflows.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/wen/opentalon/internal/review"
	"github.com/wen/opentalon/internal/review/reviewerfactory"
	"github.com/wen/opentalon/pkg/config"
	"github.com/wen/opentalon/pkg/observability"
	"github.com/wen/opentalon/tools/benchmark/internal/revieweval"
)

const defaultSampleTimeout = 2 * time.Minute

var repositoryNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "benchmark failed: %v\n", err)
		os.Exit(1)
	}
}

type commandSummary struct {
	revieweval.Summary
	Dataset         string `json:"dataset"`
	Output          string `json:"output"`
	RepositoryTools bool   `json:"repository_tools"`
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("subcommand is required; available: review")
	}
	switch args[0] {
	case "review":
		return runReview(ctx, args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown subcommand %q; available: review", args[0])
	}
}

func runReview(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("opentalon-benchmark-review", flag.ContinueOnError)
	flags.SetOutput(stderr)
	datasetPath := flags.String("dataset", "./data/processed/review-v1/pilot-15.jsonl", "review-v1 JSONL dataset")
	outputPath := flags.String("output", "./data/results/review-v1/results.jsonl", "per-candidate result JSONL")
	reviewerMode := flags.String("reviewer", "rules", "reviewer implementation: rules or agent")
	repositoriesRoot := flags.String("repositories-root", "", "downloaded dataset repositories; enables agent repository tools")
	limit := flags.Int("limit", 0, "evaluate only the first N records; 0 evaluates all")
	timeout := flags.Duration("sample-timeout", defaultSampleTimeout, "timeout for each candidate")
	pretty := flags.Bool("pretty", true, "indent the summary JSON")
	progress := flags.Bool("progress", true, "print per-candidate progress to stderr")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	if *limit < 0 {
		return fmt.Errorf("limit cannot be negative")
	}
	if *timeout <= 0 {
		return fmt.Errorf("sample-timeout must be greater than zero")
	}
	if *outputPath == "" || *outputPath == "-" {
		return fmt.Errorf("output must be a file path; stdout is reserved for the summary")
	}
	if *repositoriesRoot != "" && *reviewerMode != "agent" {
		return fmt.Errorf("repositories-root is only supported with reviewer agent")
	}

	dataset, err := os.Open(*datasetPath)
	if err != nil {
		return fmt.Errorf("open dataset %q: %w", *datasetPath, err)
	}
	defer dataset.Close()
	records, err := revieweval.ReadRecords(dataset, *limit)
	if err != nil {
		return err
	}

	factory, reviewerName, shutdown, err := buildFactory(ctx, *reviewerMode, *repositoriesRoot)
	if err != nil {
		return err
	}
	defer shutdown()

	temporary, commitOutput, cleanupOutput, err := createAtomicOutput(*outputPath)
	if err != nil {
		return err
	}
	defer cleanupOutput()

	options := revieweval.Options{ReviewerName: reviewerName, PerSampleTimeout: *timeout}
	if *progress {
		options.Progress = func(completed, total int, result revieweval.Result) {
			fmt.Fprintf(stderr, "[%d/%d] %s %s (%d ms)\n", completed, total, result.CandidateID, result.Status, result.DurationMS)
		}
	}
	summary, err := revieweval.Run(ctx, records, factory, temporary, options)
	if err != nil {
		return err
	}
	if err := commitOutput(); err != nil {
		return err
	}

	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if *pretty {
		encoder.SetIndent("", "  ")
	}
	return encoder.Encode(commandSummary{
		Summary: summary, Dataset: filepath.Clean(*datasetPath), Output: filepath.Clean(*outputPath),
		RepositoryTools: *repositoriesRoot != "",
	})
}

func buildFactory(ctx context.Context, mode, repositoriesRoot string) (revieweval.ReviewerFactory, string, func(), error) {
	if mode != "rules" && mode != "agent" {
		return nil, "", func() {}, fmt.Errorf("unsupported reviewer %q; available: rules, agent", mode)
	}
	shutdown := func() {}
	var llmConfig *config.LLMConfig
	if mode == "agent" {
		loaded, err := config.LoadLLMConfig()
		if err != nil {
			return nil, "", shutdown, fmt.Errorf("load agent reviewer config: %w", err)
		}
		llmConfig = &loaded
		if err := observability.Init(ctx, observability.LoadConfigFromEnv()); err != nil {
			return nil, "", shutdown, fmt.Errorf("initialize agent reviewer observability: %w", err)
		}
		shutdown = func() { _ = observability.Shutdown(context.Background()) }
	}

	if repositoriesRoot == "" {
		reviewerImpl, err := reviewerfactory.Build(ctx, reviewerfactory.Options{Mode: mode, LLMConfig: llmConfig})
		if err != nil {
			shutdown()
			return nil, "", func() {}, err
		}
		return func(context.Context, revieweval.Record) (review.Reviewer, error) {
			return reviewerImpl, nil
		}, reviewerImpl.Name(), shutdown, nil
	}

	root, err := filepath.Abs(repositoriesRoot)
	if err != nil {
		shutdown()
		return nil, "", func() {}, fmt.Errorf("resolve repositories-root: %w", err)
	}
	factory := func(sampleCtx context.Context, record revieweval.Record) (review.Reviewer, error) {
		repositoryRoot, err := recordRepositoryRoot(root, record)
		if err != nil {
			return nil, err
		}
		return reviewerfactory.Build(sampleCtx, reviewerfactory.Options{
			Mode: "agent", RepositoryRoot: repositoryRoot,
			BaseSHA: record.BaseSHA(), HeadSHA: record.FixCommit, LLMConfig: llmConfig,
		})
	}
	return factory, "eino-agent-reviewer/v2-repository-tools", shutdown, nil
}

func recordRepositoryRoot(root string, record revieweval.Record) (string, error) {
	if record.Selection.Rank <= 0 {
		return "", fmt.Errorf("candidate %q has no positive selection rank", record.CandidateID)
	}
	if !repositoryNamePattern.MatchString(record.Repository) {
		return "", fmt.Errorf("candidate %q has unsafe repository name %q", record.CandidateID, record.Repository)
	}
	directory := fmt.Sprintf("%02d-%s", record.Selection.Rank, strings.ReplaceAll(record.Repository, "/", "__"))
	candidateRoot := filepath.Join(root, directory)
	relative, err := filepath.Rel(root, candidateRoot)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("candidate %q repository path escapes repositories-root", record.CandidateID)
	}
	return candidateRoot, nil
}

func createAtomicOutput(path string) (*os.File, func() error, func(), error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, nil, nil, fmt.Errorf("create result directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".review-benchmark-*.tmp")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create result file: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	commit := func() error {
		if err := temporary.Sync(); err != nil {
			return fmt.Errorf("sync result file: %w", err)
		}
		if err := temporary.Close(); err != nil {
			return fmt.Errorf("close result file: %w", err)
		}
		if err := os.Rename(temporaryPath, path); err != nil {
			return fmt.Errorf("replace result file: %w", err)
		}
		committed = true
		return nil
	}
	cleanup := func() {
		if !committed {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
		}
	}
	return temporary, commit, cleanup, nil
}
