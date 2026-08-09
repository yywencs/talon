# OpenTalon Benchmark

`benchmark review` replays a `review-v1` JSONL dataset through the same review
domain used by `cmd/review`. It materializes GitHub file patches as unified
diffs, writes one result per candidate, and prints an aggregate summary.

Run the deterministic smoke baseline over all 15 pilot records:

```bash
go run ./tools/benchmark/cmd/benchmark review \
  --dataset ./data/processed/review-v1/pilot-15.jsonl \
  --reviewer rules \
  --output ./data/results/review-v1/rules.jsonl
```

Use `--limit 1` for a quick smoke test. Each candidate has a two-minute timeout
by default; change it with `--sample-timeout 5m`. Candidate failures are stored
in the output JSONL and do not prevent later records from running.

After configuring the normal Talon LLM environment, run the diff-only agent:

```bash
go run ./tools/benchmark/cmd/benchmark review \
  --reviewer agent \
  --output ./data/results/review-v1/agent.jsonl
```

To enable read-only base/head repository tools, first download the dataset
repositories as described in `data/processed/review-v1/README.md`, then run:

```bash
go run ./tools/benchmark/cmd/benchmark review \
  --reviewer agent \
  --repositories-root ./data/repos/review-v1 \
  --output ./data/results/review-v1/agent-repository.jsonl
```

The current dataset stores security fix commits and the evaluator replays them
in their original parent-to-fix direction. This command therefore provides a
pipeline/reliability baseline; it does not yet calculate vulnerability recall
or false-positive rates. Those metrics require reversed vulnerable changes and
reviewed negative samples.
