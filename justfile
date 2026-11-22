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
# `relaunch-prep` resolves ONE project root (AGENTCTL_RUNTIME_DIR's parent, else
# the main repo even from inside .worktrees/) and derives both the session name
# and the monitor pidfile from it, via the same Go helpers the session start
# path uses (tmux.SessionNameForProjectRoot, pidfile.Kill).
[private]
relaunch:
    #!/usr/bin/env bash
    set -e
    "{{bindir}}/agentctl" relaunch-prep
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

# Installs npm deps and the WebKit build on first run. Extra args go to
# `playwright test`, e.g. `just web-test -g swipe` or `just web-test --headed`.
# Browser tests for the phone web app (Playwright, WebKit, iPhone profile)
web-test *args:
    #!/usr/bin/env bash
    set -euo pipefail
    cd "{{justfile_directory()}}/internal/serve/e2e"
    [ -d node_modules/@playwright/test ] || npm ci
    npx playwright install webkit
    node ../web/logic.test.mjs
    npx playwright test {{args}}

# Fixture fleet, recorded actions, loopback only. Open the printed /__login
# URL first to get the device cookie.
# Serve the phone web app on fake backends for manual testing
web-dev addr="127.0.0.1:8787":
    go run ./internal/serve/servetest/devserver -addr {{addr}}

# Demo harness (docs/development.md, "Demo harness and screenshots"): a fake,
# isolated agentctl world on its own tmux server (-L agentctl-demo, or agentctl-demo-<hash> for a custom root) under a
# temp root, for screenshots. Nothing touches the real ~/.claude, agentctl
# config or the default tmux server.
_demo_root := env_var_or_default("AGENTCTL_DEMO_ROOT", trim_end_match(env_var_or_default("TMPDIR", "/tmp"), "/") / "agentctl-demo")
_demo_bin := _demo_root / "bin" / "agentctl-demo"
_demo_flags := "--root '" + _demo_root + "' --scenes demo/scenes --fixtures demo/transcripts"

[private]
demo-build:
    mkdir -p "{{_demo_root}}/bin"
    go build -o "{{_demo_root}}/bin/agentctl" .
    go build -o "{{_demo_bin}}" ./internal/demo/cmd/seed

# Start (or re-seed) the demo world for a scene (name in demo/scenes, or a path containing a /)
demo scene="hub": demo-build
    "{{_demo_bin}}" up {{_demo_flags}} --scene "{{scene}}"

# Stop the demo tmux server and remove the demo temp root
demo-stop:
    #!/usr/bin/env bash
    set -euo pipefail
    if [ -x "{{_demo_bin}}" ]; then
        "{{_demo_bin}}" down --root "{{_demo_root}}"
    else
        # Never built for this root, so no server of ours can be running;
        # killing a guessed label could stop another demo.
        echo "nothing to stop: no demo was started for {{_demo_root}}"
    fi

# Render one scene to demo/out/<scene>.png|gif (or to out, a path without extension)
demo-shot scene out="": demo-build
    "{{_demo_bin}}" shot {{_demo_flags}} --scene "{{scene}}" --out "{{out}}"

# Render a scene's phone: block (the phone web app over the scene's fleet) to demo/out/<scene>-phone.png
# (numbered PNGs for several screenshots, or out + -phone). Needs node; installs the Playwright WebKit
# build like web-test does.
demo-phone-shot scene out="":
    #!/usr/bin/env bash
    set -euo pipefail
    root="{{justfile_directory()}}"
    mkdir -p "{{_demo_root}}/bin"
    go build -o "{{_demo_bin}}" ./internal/demo/cmd/seed
    (cd "$root/internal/serve/e2e" && { [ -d node_modules/@playwright/test ] || npm ci; } && npx playwright install webkit)
    "{{_demo_bin}}" phone-shot --scenes demo/scenes --fixtures demo/transcripts --scene "{{scene}}" --out "{{out}}"

# Render every scene in demo/scenes
demo-shots: demo-build
    #!/usr/bin/env bash
    set -euo pipefail
    for f in demo/scenes/*.yaml; do
        "{{_demo_bin}}" shot {{_demo_flags}} --scene "$f"
    done

# Uses a temp HOME so the default theme applies and no real config is read. duration is whole seconds (e.g. 30s).
# Record `agentctl race` over the canned transcript to a GIF at 120x35 (needs vhs)
demo-race out="demo/out/race.gif" duration="30s":
    #!/usr/bin/env bash
    set -euo pipefail
    if ! command -v vhs >/dev/null 2>&1; then
        echo "vhs is not installed: brew install vhs (https://github.com/charmbracelet/vhs)" >&2
        exit 1
    fi
    root="{{_demo_root}}/race"
    mkdir -p "$root/bin" "$root/home" "$(dirname "{{out}}")"
    go build -o "$root/bin/agentctl" .
    # Agent replay + the beat before the end card + a few seconds of card.
    secs=$(( $(echo "{{duration}}" | tr -dc '0-9') + 9 ))
    cat > "$root/race.tape" <<EOF
    Output "{{out}}"
    Set Shell "bash"
    Set FontSize 14
    Set Width 1149
    Set Height 623
    Set Padding 20
    Set Theme "Catppuccin Mocha"
    Set CursorBlink false
    Hide
    Type "exec env -u TMUX -u TMUX_PANE HOME='$root/home' XDG_CONFIG_HOME='$root/home/.config' LANG=en_US.UTF-8 '$root/bin/agentctl' race internal/race/testdata/demo.jsonl --duration {{duration}}"
    Enter
    Sleep 500ms
    Show
    Sleep ${secs}s
    Type "q"
    EOF
    vhs "$root/race.tape"
    echo "wrote {{out}}"

# Validate a scene file without starting anything
demo-check scene: demo-build
    "{{_demo_bin}}" check {{_demo_flags}} --scene "{{scene}}"

# Compare a REAL Claude Code pane's structure (tmux target, default server) with the fake pane; exits non-zero on drift
demo-pane-check target: demo-build
    "{{_demo_bin}}" pane-check --target "{{target}}"
