## Bash hook safety — avoid PreToolUse errors

The PreToolUse hook enforces an allowlist. Many common shell commands are **not** pre-authorized and will be blocked with a hook error. Follow these rules to avoid them.

### Use dedicated tools — never Bash — for file operations

| Instead of... | Use... |
|---|---|
| `cat file` | `Read` tool |
| `grep pattern dir` | `Grep` tool |
| `find . -name "*.go"` | `Glob` tool |
| `ls dir/` | `Glob` tool with `path` parameter |
| `echo "..." > file` | `Write` tool |

Shell utilities **not** pre-authorized (will trigger hook errors):
`ls`, `mkdir`, `cp`, `mv`, `rm`, `touch`, `echo`, `cat`, `which`, `type`

### agentctl — always call from PATH

**Always**: `agentctl status`, `agentctl dispatch --issue 42`, etc.

**Never**:
- Building and running the source tree to invoke agentctl: broken and wasteful
- `~/.local/bin/agentctl`: hardcoded path, fragile
- Any variation involving `cd` to find the binary

`agentctl` is on PATH inside an agentctl session. Just call it directly.

### gh — pre-authorized subcommands only

**Safe to use**: `gh pr *`, `gh issue *`, `gh repo *`, `gh label *`

**Blocked** (will trigger hook errors):
- `gh run view` / `gh run list` — use `gh pr view` for PR/CI status instead
- `gh api` — blocked
- `gh release` — blocked

### Pre-authorized via global settings (always safe)

- `go *` — all go subcommands (`go mod tidy`, `go get`, `go run`, `go generate`, etc.)
- `git *` — all git subcommands
- `agentctl *` — all agentctl subcommands
