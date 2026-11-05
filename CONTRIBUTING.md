# Contributing to agentctl

Thanks for helping. This file covers building, testing, and sending a PR. For a map of the code, read [CLAUDE.md](CLAUDE.md) (it doubles as the contributor and agent guide) and [docs/architecture.md](docs/architecture.md).

## Build, test, lint

Requirements: Go 1.26+, tmux 3.4+, and [golangci-lint](https://golangci-lint.run/).

```bash
go build -o agentctl .     # build the binary
go build ./...             # compile everything
go test ./...              # run the test suite
go test -race ./...        # required for concurrent code
go vet ./...
golangci-lint run          # config in .golangci.yml
```

`just` shortcuts exist for developers: `just build` (local `./agentctl`), `just install` (installs to `~/.local/bin`, override with `BINDIR`), `just restart`, and `just logs`.

Tests that need a real tmux server or network resources are skipped unless the environment provides them. Bug fixes need a regression test that fails before the fix and passes after.

## Pull requests

- Branch from `main`; never commit directly to it.
- Keep a PR focused on one change. Keep the title short and imperative ("Fix X", "Add Y"), and the description concise.
- Reference the issue (`Closes #N`) when there is one.
- `go build ./...`, `go test ./...`, `go vet ./...`, and `golangci-lint run` must be clean.
- Commit messages use the imperative mood. Add new commits rather than amending after review starts.
- Wrap errors with context: `fmt.Errorf("doing X: %w", err)`.
- Update docs when behavior changes. Config keys are documented in [docs/configuration.md](docs/configuration.md); the `internal/config` structs are the source of truth.
- If you diverge from an agreed design, say so in a `## Deviations` section of the PR description.

The Go style baseline is the [Uber Go Style Guide](https://github.com/uber-go/guide/blob/master/style.md): table-driven tests with `t.Run`, unexported types by default, small focused functions.

## Issue labels

| Label | Purpose |
|-------|---------|
| `bug` | Something isn't working |
| `enhancement` | New feature or request |
| `quick-fix` | Small, self-contained change |
| `tmux` | Tmux interactions, status bar, keybindings |
| `monitor` | Monitor loop, state detection, transition handlers |
| `tui` | Bubble Tea popups and menus |
| `agents` | Agent discovery, creation, registry |
| `sessions` | Session log parsing, summaries, cross-context |
| `dx` | Developer experience, CLI ergonomics |
| `refactor` | Code cleanup, no behavior change |

Typical combos: `enhancement` + `tmux`, `bug` + `monitor`, `quick-fix` + `tui`.

## Security

Do not report vulnerabilities in public issues. See [SECURITY.md](SECURITY.md).

## License

By contributing you agree that your contributions are licensed under the [MIT License](LICENSE).
