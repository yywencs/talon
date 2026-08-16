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
	"github.com/wen/opentalon/internal/evaluation"
	"github.com/wen/opentalon/internal/storage"
)

type options struct {
	dataRoot       string
	datasetVersion string
	codeVersion    string
	outputDir      string
	envFile        string
	timeout        time.Duration
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintf(os.Stderr, "talon-export failed: %v\n", err)
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

	database, err := storage.OpenPostgresFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("open PostgreSQL storage: %w", err)
	}
	defer database.Close()

	manifest, err := evaluation.ExportBatch(ctx, evaluation.ExportConfig{
		Store: database.RunArtifacts(), DataRoot: opts.dataRoot,
		DatasetVersion: opts.datasetVersion, CodeVersion: opts.codeVersion, OutputDir: opts.outputDir,
	})
	if err != nil {
		return err
	}
	fmt.Printf("exported %d terminal runs to %s (code=%s dataset=%s artifact=%s)\n",
		len(manifest.Runs), opts.outputDir, manifest.CodeVersion, manifest.DatasetVersion, manifest.ArtifactSchemaVersion)
	return nil
}

func parseOptions(arguments []string) (options, error) {
	set := flag.NewFlagSet("talon-export", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	var result options
	set.StringVar(&result.dataRoot, "data-root", "data", "版本化数据集的根目录")
	set.StringVar(&result.datasetVersion, "dataset-version", "", "要导出的数据集版本，例如 toolops-v1")
	set.StringVar(&result.codeVersion, "code-version", "", "要导出的代码版本，例如 Git commit")
	set.StringVar(&result.outputDir, "output", "", "新建的导出目录")
	set.StringVar(&result.envFile, "env-file", ".env", "存在时加载 DATABASE_DSN 的环境变量文件；空值表示不加载")
	set.DurationVar(&result.timeout, "timeout", time.Minute, "导出操作的时间上限")
	if err := set.Parse(arguments); err != nil {
		return options{}, err
	}
	if set.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected positional arguments: %v", set.Args())
	}
	if result.datasetVersion == "" {
		return options{}, fmt.Errorf("dataset-version is required")
	}
	if result.codeVersion == "" {
		return options{}, fmt.Errorf("code-version is required")
	}
	if result.outputDir == "" {
		return options{}, fmt.Errorf("output is required")
	}
	if result.timeout <= 0 {
		return options{}, fmt.Errorf("timeout must be positive")
	}
	return result, nil
}
