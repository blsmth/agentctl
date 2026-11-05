# AgentctlFleet (experimental)

An experimental, macOS-only companion app for agentctl. It shows every live agentctl session in a native window with embedded terminals attached to the agents' tmux windows. Expect rough edges; it is not required to use agentctl and is not part of the Go release artifacts.

It discovers sessions by reading the agentctl registry (`~/.local/state/agentctl/registry/<session>.json`, written by each session's monitor) and each project's `state.json`, and talks to tmux over control mode. agentctl must be installed and have at least one running session.

## Requirements

- macOS 14+
- Swift 6 toolchain (Xcode or Command Line Tools)
- tmux 3.4+ and agentctl on the machine

## Build

```bash
cd macapp
swift build            # debug build
swift test             # run tests
./build.sh             # release build assembled into build/AgentctlFleet.app
```

`build.sh` runs `swift build -c release`, assembles a minimal app bundle, and **ad-hoc signs** it so it can launch outside a terminal. It is not notarized, so macOS may ask you to approve the first launch (right-click, Open). The app is deliberately not sandboxed: it executes `tmux` and must reach tmux's control socket under `/private/tmp`.

CI runs `swift build`, a release build, and `swift test` (the `macapp` job in `.github/workflows/ci.yaml`) when `macapp/` changes.
