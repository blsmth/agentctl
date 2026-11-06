default:
    @just --list

# Install directory. Override with BINDIR=/some/dir just install.
bindir := env_var_or_default("BINDIR", env_var("HOME") / ".local/bin")

_ldflags := "-X main.commit=$(git rev-parse --short HEAD) -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# Build and install to $BINDIR (default ~/.local/bin)
# Build to a temp file then mv to avoid macOS SIGKILL on arm64 when
# overwriting a running binary (code signature invalidation).
install:
    mkdir -p "{{bindir}}"
    go build -ldflags "{{_ldflags}}" -o "{{bindir}}/agentctl.tmp" .
    mv -f "{{bindir}}/agentctl.tmp" "{{bindir}}/agentctl"

# Build locally (no install)
build:
    go build -ldflags "{{_ldflags}}" -o agentctl .

# Kill the tmux session and relaunch (no build)
# Session name mirrors tmux.SetSessionName: "agentctl-<sanitized-cwd-basename>",
# falling back to plain "agentctl" when the basename sanitizes to empty.
[private]
relaunch:
    #!/usr/bin/env bash
    safe=$(basename "$PWD" | tr -c 'a-zA-Z0-9_-' '-' | sed 's/^-*//;s/-*$//')
    session="agentctl${safe:+-$safe}"
    # Stop the old monitor first: if it outlives the session it can observe
    # the replacement (same name) and overwrite state.json with zero agents.
    pidfile="${AGENTCTL_RUNTIME_DIR:-$PWD/.agentctl}/monitor.pid"
    if [ -f "$pidfile" ]; then
        pid=$(tr -dc '0-9' < "$pidfile")
        if [ -n "$pid" ] && ps -p "$pid" -o args= 2>/dev/null | grep -q 'agentctl.*monitor'; then
            kill "$pid" 2>/dev/null || true
            for _ in 1 2 3 4 5 6 7 8 9 10; do
                kill -0 "$pid" 2>/dev/null || break
                sleep 0.1
            done
        fi
    fi
    tmux kill-session -t "$session" 2>/dev/null || true
    sleep 0.5
    "{{bindir}}/agentctl"

# Build, install, and restart the tmux session
restart: install relaunch

# Maintainer-only (darwin/arm64): download the CI-built binary for origin/main HEAD from GitHub Actions and restart.
# Everyone else should use `just install`.
# The artifact zip contains a nested "agentctl_darwin_arm64_vN/agentctl" path,
# so it must be extracted to a temp dir and the binary located inside it.
# Extracting straight into ~/.local/bin silently left the running binary in
# place (and failed outright on the second run, once the nested dir existed).
upgrade:
    #!/usr/bin/env bash
    set -euo pipefail
    [ "$(uname -sm)" = "Darwin arm64" ] || { echo "upgrade: darwin/arm64 only; use just install" >&2; exit 1; }
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    # Resolve the CI run for the current origin/main HEAD. A bare
    # `gh run download -n ...` grabs the newest artifact of that name, which can
    # belong to an older commit (or a failed run), so pin to an exact run ID.
    git fetch --quiet origin main
    sha=$(git rev-parse origin/main)
    run_id=$(gh run list --repo blsmth/agentctl --workflow ci.yaml --branch main --status success --limit 50 \
        --json databaseId,headSha \
        --jq "[.[] | select(.headSha == \"$sha\")][0].databaseId // empty")
    if [ -z "$run_id" ]; then
        echo "upgrade: no successful CI run on main for origin/main HEAD $sha" >&2
        echo "upgrade: wait for CI to finish (or fix it), or use 'just install' to build locally" >&2
        exit 1
    fi
    echo "upgrade: downloading artifact from run $run_id ($sha)"
    if ! gh run download "$run_id" -n agentctl-darwin-arm64 --repo blsmth/agentctl -D "$tmp"; then
        echo "upgrade: could not download the artifact from run $run_id (it may have expired; CI retains artifacts for 14 days)" >&2
        echo "upgrade: use 'just install' to build locally instead" >&2
        exit 1
    fi
    bin=$(find "$tmp" -type f -name agentctl | head -1)
    if [ -z "$bin" ]; then
        echo "upgrade: no 'agentctl' binary found in the artifact" >&2
        exit 1
    fi
    chmod +x "$bin"
    # mv to a temp name first: macOS arm64 SIGKILLs a running binary that is
    # overwritten in place (code signature invalidation), same as `install`.
    mkdir -p "{{bindir}}"
    mv -f "$bin" "{{bindir}}/agentctl.tmp"
    mv -f "{{bindir}}/agentctl.tmp" "{{bindir}}/agentctl"
    just relaunch


# Tail the shared monitor log (see internal/logging.MonitorLogPath).
# Honors AGENTCTL_RUNTIME_DIR the same way paths.Init does, falling back to
# the repo-relative .agentctl/logs/agentctl.log for whichever project
# directory `just` is run from.
logs:
    #!/usr/bin/env bash
    set -euo pipefail
    runtime_dir="${AGENTCTL_RUNTIME_DIR:-{{justfile_directory()}}/.agentctl}"
    tail -f "$runtime_dir/logs/agentctl.log"
