# tmux as Agent Runtime: Why It's the Secret Weapon

## The insight

Most AI agent frameworks treat the runtime as an API abstraction — agents are HTTP calls, queue consumers, or subprocess pipes. agentctl treats tmux as the runtime. Every agent is a tmux window. Every interaction is a tmux command. This sounds like a hack. It's actually the most powerful design decision in the system.

## What tmux gives you for free

### 1. Full agent observability with zero instrumentation

```bash
tmux capture-pane -t agentctl-agents:@138 -p -S -20
```

That's it. You can see exactly what any agent is doing — the last 20 lines of its terminal output — without the agent knowing or caring. No logging SDK, no telemetry pipeline, no structured output format. The agent writes to a terminal. You read the terminal. The agent doesn't even know you're watching.

This is how we diagnosed a stuck agent today: `capture-pane`, see the permission prompt, fix it. Total time: 5 seconds.

### 2. Remote control without agent cooperation

```bash
tmux send-keys -t agentctl-agents:@139 "retry" Enter
```

Agent hit an API error and is sitting idle? Send it a keystroke. Need to accept a permission prompt? Send `"2"` (the menu option for "accept all edits"). Need to kill a runaway agent? `Ctrl-C` via send-keys. Need to ask it a question mid-session? Just type into its pane.

The agent doesn't need a "remote control API." The agent doesn't need a health check endpoint. The agent doesn't need a restart hook. tmux gives you all of this through one primitive: send keystrokes to a pane.

### 3. Session persistence and recovery

tmux sessions survive SSH disconnects, terminal crashes, and closing your laptop lid. An agent that's been running for 2 hours doesn't die because you accidentally closed iTerm. You reattach and it's still there, still working.

This matters enormously for long-running dispatches. A go-engineer working on a complex feature might run for 30+ minutes. If that lived in a subprocess, any parent process crash kills it. In tmux, it's an independent session. The monitor can crash and restart without affecting running agents.

### 4. Multiplexing without concurrency bugs

Each agent is an isolated tmux window. They share nothing — no memory, no file handles, no event loops. "Running 7 agents in parallel" isn't a threading problem. It's just 7 windows in a session. The OS handles scheduling. tmux handles display. There are no race conditions because there's no shared state.

The monitor reads agent state by examining tmux window properties and file-based state files. It never injects into an agent's process. The architecture is inherently safe because the boundary between "monitor" and "agent" is the tmux window, which is an OS-level isolation boundary.

### 5. Cross-session targeting

tmux's targeting system (`-t session:window.pane`) works across sessions. This means:

```bash
# From ANY terminal, ANY session, ANY context:
tmux capture-pane -t agentctl-agents:@138 -p    # read agent output
tmux send-keys -t agentctl-agents:@138 "help"   # send it input
tmux list-windows -t agentctl-agents             # list all agents
```

Today we dispatched agents that accidentally landed in the wrong session (`agentctl-agents` instead of `agentctl-agentctl`). The agents were invisible in the tab bar. But we could still see them, control them, and read their output — because tmux targeting doesn't care which session you're in. The agents were fully operational; only the UI was wrong.

This is the difference between "the agent is broken" and "the agent is in the wrong tab." That distinction only exists because tmux separates the runtime (the window) from the presentation (the session/tab bar).

### 6. Window metadata as state store

tmux windows carry environment variables that persist for the lifetime of the window:

```bash
AGENTCTL_AGENT_NAME=go-engineer
AGENTCTL_WINDOW_ID=@138
AGENTCTL_TICKET=560
AGENTCTL_MODE=dispatch
```

These env vars are set at launch and survive across every command the agent runs. The hooks read them to know which agent they're running in. The monitor reads them to build the tab bar. No database, no config file, no API call — just environment variables on a tmux window.

### 7. Hooks via tmux's native event system

tmux has built-in hooks for window events. agentctl registers two session-scoped hooks (see `chromeBarHookArgs` and `activeWindowHookArgs` in `internal/tmux/tmux.go`):

```bash
# every new window gets a chrome bar pane (chrome-bar-pane itself skips the
# utility windows tickets|sessions|agents)
set-hook -t <session> after-new-window \
  "run-shell -b -c <projectRoot> 'AGENTCTL_RUNTIME_DIR=<dir> agentctl chrome-bar-pane #{window_id}'"

# record the active window so the monitor and chrome bar know which tab is focused
set-hook -t <session> after-select-window \
  "run-shell -b 'AGENTCTL_RUNTIME_DIR=<dir> agentctl set-active-window #{window_id}'"
```

Only `#{window_id}` is ever interpolated into these commands. tmux expands formats before the shell runs, so a format such as `#{window_name}` would splice an attacker-chosen agent name into shell text no matter how it is quoted; likewise every path in them has `#` escaped as `##` (`tmux.FormatEscape`).

When you switch tabs, tmux fires the hook and the active-window file is updated. The tab bar highlights the active window because tmux tells us when the active window changes. (The commands above are abridged; the exact quoting lives in the functions named above.)

## The compound effect

Any one of these capabilities is nice. Together, they're transformative:

- **Dispatch** = create a tmux window with env vars → launch Claude in it
- **Monitor** = read window state files every 500ms → update tab bar
- **Observe** = `capture-pane` to see what any agent is doing
- **Intervene** = `send-keys` to unstick a blocked agent
- **Coordinate** = from one agent, dispatch and monitor others via tmux commands
- **Recover** = sessions persist across crashes; reattach and continue

We used every single one of these today in a single session. We dispatched agents, watched them work via capture-pane, sent keystrokes to accept permission prompts, retried failed sessions, killed stuck windows, and checked on progress — all without the agents having any awareness that they were being managed.

## Why not Docker / k8s / subprocess / etc.?

**Docker**: Great isolation, terrible interactivity. You can't send a keystroke to a Docker container and watch it type into a Claude prompt. You can't capture the last 20 lines of a TUI running inside a container without instrumenting the application. Docker is for services. tmux is for interactive agents.

**Kubernetes**: Same problems as Docker but worse. The overhead of a pod per agent is absurd for a local dev tool. And good luck doing `kubectl exec` to send a keystroke to a Bubble Tea TUI.

**Raw subprocesses**: No persistence (parent dies, child dies), no multiplexing UI, no remote access, no session recovery. You'd have to build all of that yourself. tmux already did it.

**Screen**: tmux but worse in every way that matters. No scriptable targeting, no structured output, no hook system, less active development.

## The mental model

Think of tmux not as a terminal multiplexer, but as a lightweight process orchestrator with built-in:
- Process isolation (windows)
- Service discovery (window names + targeting)
- State management (window env vars + per-window state files)
- Observability (capture-pane)
- Remote control (send-keys)
- Event system (hooks)
- Persistence (session survival)

agentctl doesn't compete with tmux. It stands on top of it. The monitor, the tab bar, the dispatch system — they're all thin layers over tmux primitives. The power comes from tmux. agentctl just makes it ergonomic.

That's why the design works. We didn't build an agent runtime. We noticed that one already existed.
