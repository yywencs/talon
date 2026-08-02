# OpenTalon Review Entry

`cmd/review` is an isolated code-review entry point. Its default rules mode does
not initialize an LLM. Agent mode initializes only the Eino reviewer; neither
mode starts the interactive Agent session or sandbox, so the existing
`go run .` path is unaffected.

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

The command emits the same versioned JSON report from either the deterministic
rule reviewer or the Eino single-model reviewer.

Run the Eino reviewer with the same LLM environment variables as the main
OpenTalon agent. Supplying `--repository-root` enables the request-scoped
read-only repository tools:

```bash
git diff BASE_SHA HEAD_SHA | go run ./cmd/review \
  --reviewer agent \
  --repository owner/repository \
  --repository-root /absolute/path/to/local/repository \
  --base-sha BASE_SHA \
  --head-sha HEAD_SHA
```

`--reviewer rules` remains the default and never initializes an LLM. The agent
reviewer formats the parsed diff, invokes the configured chat model, decodes
JSON findings, and verifies every reported path and line against the supplied
diff. With `--repository-root`, an Eino ReAct loop lets the model read bounded
base/head file ranges, search Go symbols, and list repository files. Omitting
the flag preserves the original single-model path.

The repository tools read Git objects only. They do not checkout commits,
modify the worktree, execute repository code, or provide an execution sandbox.
Both `--base-sha` and `--head-sha` must be full 40-character commit SHAs that
already exist in the local repository.

The default input limit is 2 MiB and can be changed with `--max-diff-bytes`.
