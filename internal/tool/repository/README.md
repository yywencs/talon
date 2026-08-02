# Read-only repository context

`repository` gives an agent bounded access to source code without exposing a shell or mutable worktree.

```go
reader, err := repository.NewGitReader(ctx, repository.GitConfig{
    RepositoryRoot: localRepository,
    BaseSHA:        request.BaseSHA,
    HeadSHA:        request.HeadSHA,
})

snippet, err := reader.ReadFile(ctx, repository.RevisionHead, "util/url.go", 20, 100)
matches, err := reader.SearchSymbol(ctx, repository.RevisionHead, "URLEscape")
files, err := reader.ListFiles(ctx, repository.RevisionHead, "util")
```

The reader accepts only the `base` and `head` aliases bound at construction. It uses offline Git object reads, never checks out a revision, and never executes repository hooks or code.

Security controls include:

- full 40-character trusted commit SHAs;
- no arbitrary revisions, regexes, pathspecs, or shell strings;
- rejection of absolute paths, `..`, `.git`, backslashes, control characters, symlinks, submodules, binary files, and invalid UTF-8;
- fixed limits for file bytes, line ranges, search results, listed files, and Git output;
- disabled lazy fetching, replacement objects, global Git config, and terminal prompting.

This is a read boundary, not an execution sandbox. Compilation, tests, static analyzers, and repository scripts must run in the Docker sandbox added in a later stage.
