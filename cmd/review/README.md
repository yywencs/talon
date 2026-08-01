# OpenTalon Review Entry

`cmd/review` is an isolated code-review entry point. It does not initialize the
interactive Agent session, an LLM provider, or the sandbox, so it cannot change
the behavior of the existing `go run .` path.

Review a saved unified diff:

```bash
go run ./cmd/review \
  --diff ./change.diff \
  --repository owner/repository \
  --pr 123 \
  --base-sha BASE_SHA \
  --head-sha HEAD_SHA
```

Review a diff from stdin:

```bash
git diff origin/main...HEAD | go run ./cmd/review --repository owner/repository
```

The command currently runs a deterministic rule reviewer and emits a versioned
JSON report. The same `review.Reviewer` interface can later host an Eino graph,
while preserving the CLI and report contract.

The default input limit is 2 MiB and can be changed with `--max-diff-bytes`.
