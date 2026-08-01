// dataset 是 OpenTalon 的离线数据预处理命令，不参与 Review Runtime 的在线执行。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/wen/opentalon/tools/dataset/internal/githubapi"
	"github.com/wen/opentalon/tools/dataset/internal/govulndb"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "dataset failed: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("subcommand is required; available: govulndb-filter, govulndb-enrich, govulndb-select")
	}
	switch args[0] {
	case "govulndb-filter":
		return runGoVulnDBFilter(ctx, args[1:], stdout, stderr)
	case "govulndb-enrich":
		return runGoVulnDBEnrich(ctx, args[1:], stdout, stderr)
	case "govulndb-select":
		return runGoVulnDBSelect(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown subcommand %q; available: govulndb-filter, govulndb-enrich, govulndb-select", args[0])
	}
}

// runGoVulnDBSelect 只消费本地 enriched JSONL，不访问网络，也不需要 GitHub Token。
func runGoVulnDBSelect(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("govulndb-select", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("input", "./data/interim/go-vulndb/enriched-candidates.jsonl", "enriched candidate JSONL input")
	output := flags.String("output", "./data/processed/review-v1/pilot-15.jsonl", "selected candidate JSONL output")
	size := flags.Int("size", 15, "number of unique repository/advisory records to select")
	seed := flags.String("seed", "opentalon-pilot-v1", "stable tie-break seed")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	stats, err := govulndb.SelectFile(govulndb.SelectOptions{
		InputPath: *input, OutputPath: *output, Size: *size, Seed: *seed,
	})
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(stats); err != nil {
		return fmt.Errorf("encode selection stats: %w", err)
	}
	return nil
}

func runGoVulnDBEnrich(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("govulndb-enrich", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("input", "./data/interim/go-vulndb/candidates.jsonl", "filtered candidate JSONL input")
	output := flags.String("output", "./data/interim/go-vulndb/enriched-candidates.jsonl", "enriched candidate JSONL output")
	cache := flags.String("cache", "./data/cache/github/commits", "immutable GitHub commit response cache")
	concurrency := flags.Int("concurrency", 4, "number of concurrent GitHub requests")
	limit := flags.Int("limit", 0, "process only the first N deterministic candidates; 0 means all")
	maxFiles := flags.Int("max-files", 8, "maximum changed files for a materializable candidate")
	maxChanges := flags.Int("max-changes", 400, "maximum changed lines for a materializable candidate")
	progress := flags.Bool("progress", true, "print progress to stderr")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}

	token, err := githubapi.ResolveToken(ctx)
	if err != nil {
		return err
	}
	client, err := githubapi.NewClient(githubapi.Config{Token: token, CacheDir: *cache})
	if err != nil {
		return err
	}
	options := govulndb.EnrichOptions{
		InputPath: *input, OutputPath: *output, Concurrency: *concurrency,
		Limit: *limit, MaxFiles: *maxFiles, MaxChanges: *maxChanges,
	}
	if *progress {
		options.Progress = func(done, total int) {
			if done == total || done%25 == 0 {
				fmt.Fprintf(stderr, "enrich progress: %d/%d\n", done, total)
			}
		}
	}
	stats, err := govulndb.EnrichFile(ctx, client, options)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(stats); err != nil {
		return fmt.Errorf("encode enrichment stats: %w", err)
	}
	return nil
}

func runGoVulnDBFilter(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("govulndb-filter", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("input", "./data/raw/go-vulndb/ID", "directory containing Go vulnerability OSV JSON files")
	output := flags.String("output", "./data/interim/go-vulndb/candidates.jsonl", "candidate JSONL output path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}

	stats, err := govulndb.FilterDirectory(ctx, *input, *output)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(stats); err != nil {
		return fmt.Errorf("encode filter stats: %w", err)
	}
	return nil
}
