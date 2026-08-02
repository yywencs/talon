package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/wen/opentalon/internal/review"
	"github.com/wen/opentalon/internal/review/agentreview"
	"github.com/wen/opentalon/internal/tool/repository"
	"github.com/wen/opentalon/pkg/config"
	"github.com/wen/opentalon/pkg/observability"
)

const defaultMaxDiffBytes int64 = 2 << 20

func main() {
	// 该命令是独立旁路：rules 模式不加载 LLM；agent 模式只加载 Eino Reviewer，
	// 两种模式都不会启动主 CLI 的交互 Session 或 Sandbox。
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "review failed: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	// 将输入输出抽象为 io 接口，使 CLI 不启动子进程也能完成端到端测试。
	flags := flag.NewFlagSet("opentalon-review", flag.ContinueOnError)
	flags.SetOutput(stderr)
	diffPath := flags.String("diff", "-", "path to a unified diff, or - to read stdin")
	repository := flags.String("repository", "", "repository name, for example owner/repo")
	repositoryRoot := flags.String("repository-root", "", "local Git repository used by agent read-only tools")
	pullRequest := flags.Int("pr", 0, "pull request number")
	baseSHA := flags.String("base-sha", "", "base commit SHA")
	headSHA := flags.String("head-sha", "", "head commit SHA")
	language := flags.String("language", "", "primary language hint")
	reviewerMode := flags.String("reviewer", "rules", "reviewer implementation: rules or agent")
	pretty := flags.Bool("pretty", true, "indent JSON output")
	maxBytes := flags.Int64("max-diff-bytes", defaultMaxDiffBytes, "maximum accepted diff size")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	if *maxBytes <= 0 {
		return fmt.Errorf("max-diff-bytes must be greater than zero")
	}

	diff, err := readDiff(*diffPath, stdin, *maxBytes)
	if err != nil {
		return err
	}
	reviewerImpl, err := buildReviewer(ctx, *reviewerMode, *repositoryRoot, *baseSHA, *headSHA)
	if err != nil {
		return err
	}
	if *reviewerMode == "agent" {
		// buildReviewer 已加载 .env，此时初始化才能同时读取其中的 OBS_* 配置和脱敏规则。
		if err := observability.Init(ctx, observability.LoadConfigFromEnv()); err != nil {
			return fmt.Errorf("initialize agent reviewer observability: %w", err)
		}
		defer func() { _ = observability.Shutdown(context.Background()) }()
	}
	service := review.NewService(reviewerImpl)
	report, err := service.Review(ctx, review.Request{
		Repository: *repository, PullRequest: *pullRequest,
		BaseSHA: *baseSHA, HeadSHA: *headSHA, Language: *language, Diff: diff,
	})
	if err != nil {
		return err
	}

	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if *pretty {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	return nil
}

func buildReviewer(ctx context.Context, mode, repositoryRoot, baseSHA, headSHA string) (review.Reviewer, error) {
	switch mode {
	case "rules":
		return review.NewRuleReviewer(), nil
	case "agent":
		llmConfig, err := config.LoadLLMConfig()
		if err != nil {
			return nil, fmt.Errorf("load agent reviewer config: %w", err)
		}
		if repositoryRoot == "" {
			reviewerImpl, err := agentreview.NewFromConfig(ctx, llmConfig)
			if err != nil {
				return nil, fmt.Errorf("initialize agent reviewer: %w", err)
			}
			return reviewerImpl, nil
		}
		reader, err := repository.NewGitReader(ctx, repository.GitConfig{
			RepositoryRoot: repositoryRoot,
			BaseSHA:        baseSHA,
			HeadSHA:        headSHA,
		})
		if err != nil {
			return nil, fmt.Errorf("initialize read-only repository tools: %w", err)
		}
		reviewerImpl, err := agentreview.NewFromConfigWithRepository(ctx, llmConfig, reader)
		if err != nil {
			return nil, fmt.Errorf("initialize repository agent reviewer: %w", err)
		}
		return reviewerImpl, nil
	default:
		return nil, fmt.Errorf("unsupported reviewer %q; available: rules, agent", mode)
	}
}

func readDiff(path string, stdin io.Reader, maxBytes int64) (string, error) {
	var (
		reader io.Reader = stdin
		file   *os.File
	)
	if path != "-" {
		opened, err := os.Open(path)
		if err != nil {
			return "", fmt.Errorf("open diff %q: %w", path, err)
		}
		file = opened
		reader = opened
		defer file.Close()
	}
	if reader == nil {
		return "", errors.New("diff input is unavailable")
	}

	// 多读取一个字节，用于区分“刚好达到上限”和“已经超过上限”。
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return "", fmt.Errorf("read diff: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return "", fmt.Errorf("diff exceeds %d-byte limit", maxBytes)
	}
	return string(data), nil
}
