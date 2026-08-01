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
)

const defaultMaxDiffBytes int64 = 2 << 20

func main() {
	// 该命令是独立旁路，不加载主 CLI 的配置、LLM、Session 或 Sandbox。
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
	pullRequest := flags.Int("pr", 0, "pull request number")
	baseSHA := flags.String("base-sha", "", "base commit SHA")
	headSHA := flags.String("head-sha", "", "head commit SHA")
	language := flags.String("language", "", "primary language hint")
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
	// 入口只依赖 Reviewer 接口；后续接入 Eino 时可以替换实现而不改变 CLI 契约。
	service := review.NewService(review.NewRuleReviewer())
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
