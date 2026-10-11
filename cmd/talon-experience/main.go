// talon-experience is an offline operator CLI. It exposes no publishing tools to the Agent.
package main

import (
	"context"
	"encoding/json"
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
	"github.com/wen/opentalon/internal/experience"
	"github.com/wen/opentalon/internal/experienceeval"
	"github.com/wen/opentalon/internal/llm"
	"github.com/wen/opentalon/internal/runmeta"
	"github.com/wen/opentalon/internal/storage"
)

func main() {
	if e := run(os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: talon-experience snapshot|extract|review|import-candidate|list|stage|index|plan|validate|publish|rollback|active [flags]")
	}
	command := args[0]
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	datasetRoot := f.String("dataset", "data/toolops-v1", "matching source dataset for deterministic extraction assessment")
	env := f.String("env-file", ".env", "environment file")
	sqlite := f.String("sqlite-test-db", "", "isolated SQLite test storage; production uses DATABASE_DSN")
	candidateFile := f.String("file", "", "reviewed candidate JSON for merge/revision; still requires review and validation")
	runs := f.String("run-ids", "", "comma-separated completed/failed run IDs")
	keys := f.String("candidates", "", "comma-separated immutable candidate keys")
	id := f.String("release", "", "immutable release ID")
	parent := f.String("parent", "", "expected active parent release; empty for first release")
	reportID := f.String("report", "", "passing validation report ID")
	catalogPath := f.String("catalog", "", "reviewed scenario catalog JSON")
	kind := f.String("kind", "candidate", "list: snapshot, candidate, extraction, release, report, review")
	repeats := f.Int("repeats", 3, "paired repeats; minimum 2")
	tokenRatio := f.Float64("max-token-ratio", 1.25, "maximum candidate/baseline total token ratio")
	output := f.String("output-dir", ".opentalon/experience-validation", "durable isolated trial artifacts")
	python := f.String("python", "python3", "evaluator Python executable")
	evaluatorDir := f.String("evaluator-dir", "evaluator/src", "Python package root")
	timeout := f.Duration("trial-timeout", 5*time.Minute, "per-trial timeout")
	if e := f.Parse(args[1:]); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *env != "" {
		if e := godotenv.Load(*env); e != nil && !os.IsNotExist(e) {
			return e
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var db *storage.Storage
	var e error
	if *sqlite != "" {
		db, e = storage.OpenSQLite(ctx, *sqlite)
	} else {
		db, e = storage.OpenPostgresFromEnv(ctx)
	}
	if e != nil {
		return e
	}
	defer db.Close()
	store := db.Experiences()
	print := func(v any) error { enc := json.NewEncoder(os.Stdout); enc.SetIndent("", "  "); return enc.Encode(v) }
	if command == "list" {
		switch *kind {
		case "snapshot", "candidate", "extraction", "release", "report", "review":
		default:
			return errors.New("unsupported list kind")
		}
		v, e := store.List(ctx, *kind)
		if e != nil {
			return e
		}
		return print(v)
	}
	if command == "active" {
		s := &experience.Service{Store: store}
		v, e := s.Active(ctx)
		if e != nil {
			return e
		}
		return print(map[string]string{"release": v})
	}
	if command == "import-candidate" {
		b, e := os.ReadFile(*candidateFile)
		if e != nil {
			return e
		}
		var c experience.Card
		if e = experience.DecodeStrict(b, &c); e != nil {
			return e
		}
		if e = experience.CheckSources(ctx, store, c); e != nil {
			return e
		}
		return experience.Save(ctx, store, "candidate", c.Key(), c)
	}
	if command == "review" {
		cfg, e := config.LoadLLMConfig()
		if e != nil {
			return e
		}
		m, e := llm.NewChatModel(ctx, cfg)
		if e != nil {
			return e
		}
		for _, key := range split(*keys) {
			v, e := experience.ReviewCandidate(ctx, store, m, cfg.Provider+"/"+cfg.Model, key, *parent, split(*keys)...)
			if e != nil {
				return e
			}
			if e = print(v); e != nil {
				return e
			}
		}
		return nil
	}
	if command == "snapshot" || command == "extract" {
		runIDs := split(*runs)
		if len(runIDs) == 0 {
			return errors.New("--run-ids must contain at least one terminal run ID")
		}
		snapshots := []experience.Snapshot{}
		seen := map[string]bool{}
		for _, run := range runIDs {
			if seen[run] {
				continue
			}
			seen[run] = true
			a, e := db.RunArtifacts().Get(ctx, run)
			if e != nil {
				return e
			}
			if a.Outcome != "completed" && a.Outcome != "failed" {
				return fmt.Errorf("run %q is still %s; only terminal runs can be snapshotted", run, a.Outcome)
			}
			assessment, e := experienceeval.Assess(ctx, a, *datasetRoot, *python, *evaluatorDir)
			if e != nil {
				return e
			}
			snapshot, e := experience.Freeze(ctx, store, a, assessment)
			if e != nil {
				return e
			}
			snapshots = append(snapshots, snapshot)
		}
		if command == "snapshot" {
			type savedSnapshot struct {
				Digest   string              `json:"snapshot_digest"`
				Snapshot experience.Snapshot `json:"snapshot"`
			}
			output := []savedSnapshot{}
			for _, snapshot := range snapshots {
				output = append(output, savedSnapshot{experience.Digest(snapshot), snapshot})
			}
			return print(output)
		}
		cfg, e := config.LoadLLMConfig()
		if e != nil {
			return e
		}
		m, e := llm.NewChatModel(ctx, cfg)
		if e != nil {
			return e
		}
		result, e := experience.Extract(ctx, store, m, cfg.Provider+"/"+cfg.Model, snapshots)
		if pe := print(result); pe != nil {
			return pe
		}
		return e
	}

	if command == "plan" {
		r, e := experience.Load[experience.Release](ctx, store, "release", *id)
		if e != nil {
			return e
		}
		c, e := readCatalog(*catalogPath)
		if e != nil {
			return e
		}
		plan, e := (&experience.Service{Store: store}).Plan(ctx, r, c)
		if e != nil {
			return e
		}
		return print(plan)
	}
	service, e := experience.ServiceFromEnv(store)
	if e != nil {
		return e
	}
	switch command {
	case "stage":
		v, e := service.Stage(ctx, *id, *parent, split(*keys))
		if e != nil {
			return e
		}
		return print(v)
	case "index":
		return service.IndexRelease(ctx, *id)
	case "publish":
		return service.Publish(ctx, *id, *reportID)
	case "rollback":
		return service.Rollback(ctx, *id, *parent)
	case "validate":
		catalog, e := readCatalog(*catalogPath)
		if e != nil {
			return e
		}
		cfg, e := config.LoadLLMConfig()
		if e != nil {
			return e
		}
		m, e := llm.NewChatModel(ctx, cfg)
		if e != nil {
			return e
		}
		runner := &experienceeval.Runner{Config: app.Config{Model: m, PromptDirectory: cfg.PromptsDir, RunConfig: runmeta.Config{ModelProvider: cfg.Provider, Model: cfg.Model}}, Service: service, Python: *python, EvaluatorDir: *evaluatorDir, OutputDir: *output, Timeout: *timeout}
		v, e := service.Validate(ctx, *id, catalog, experience.Policy{Repeats: *repeats, MaxTokenRatio: *tokenRatio}, runner)
		if pe := print(v); pe != nil {
			return pe
		}
		if e != nil {
			return e
		}
		if !v.Passed {
			return errors.New("validation did not pass; report retained; release remains unpublished")
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}
func split(s string) []string {
	out := []string{}
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
func readCatalog(path string) ([]experience.Case, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	var c struct {
		ReviewedBy string            `json:"reviewed_by"`
		Cases      []experience.Case `json:"cases"`
	}
	if e = experience.DecodeStrict(b, &c); e != nil {
		return nil, e
	}
	if strings.TrimSpace(c.ReviewedBy) == "" {
		return nil, errors.New("catalog must be reviewed independently; reviewed_by is required")
	}
	return c.Cases, nil
}
