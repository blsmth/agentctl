# Multiple Claude accounts

Status: proposal, 2026-11-17. Nothing here is approved yet. Related: [cron.md](cron.md), [multi-harness.md](multi-harness.md) (PR #88).

## Goal

Run each agent under the right Claude account automatically: Lincoln Loop work on the LL Team seat, personal projects (agentctl, foxden, …) on Brendan's own Max plan. Today every window uses whatever `~/.claude` is logged into, which is the LL Team seat (`claude auth status`: `orgName: Lincoln Loop`, `subscriptionType: team`).

## Is it possible with OAuth? Yes, and it's the documented way

From [Authentication → Log in with multiple accounts](https://code.claude.com/docs/en/authentication.md):

> To stay signed in to multiple accounts at once, such as work and personal accounts, give each account its own configuration directory. When you start `claude`, set the `CLAUDE_CONFIG_DIR` environment variable to the directory for the account you want to use. Each directory has its own settings, session history, and claude.ai login or API key.

And from [Credential management](https://code.claude.com/docs/en/authentication.md#credential-management):

> If you've set the `CLAUDE_CONFIG_DIR` environment variable, Claude Code keeps the `.credentials.json` file under that directory instead … and keys the macOS Keychain entry to that directory too, so a session with a different `CLAUDE_CONFIG_DIR` reads a different entry.

So each account is a directory. Claude Code runs its own OAuth flow (`claude auth login`) per directory, stores and refreshes the token itself, and agentctl only chooses which directory a window gets. **agentctl never sees a token.** That matters for the terms: [Legal and compliance](https://code.claude.com/docs/en/legal-and-compliance) forbids third-party tools from collecting, storing or intermediating Claude.ai credentials.

### Rejected alternative: `claude setup-token` + `CLAUDE_CODE_OAUTH_TOKEN`

A one-year token per account, injected as env per window, would work technically. But:

- agentctl would have to store the token, which is exactly the "collect, store or intermediate" line. It's fine for a user pasting their own token into their own CI, but not for a tool published on GitHub.
- The token "can only make model requests": no Remote Control, no claude.ai connectors. Cross-machine `ListAgents` and the phone flows that ride on Remote Control would break.
- Tokens put in env leak into child processes and `ps`.

Use config dirs. Mention `setup-token` only as something a user may do themselves for headless cron on a box with no keychain.

### Terms guardrails

- **Your own accounts, your own work.** Two of your own subscriptions on your own machine is plainly fine. The consumer terms ban *sharing* an account, not having two.
- **No automatic failover between subscriptions to dodge limits.** "When personal Max hits 5h, silently continue on the LL Team seat" (or across several personal plans) reads as bypassing usage limits ("bypassing any of our systems or protective measures", Consumer Terms §3). For the LL seat it also bills an employer for personal work. agentctl only routes by explicit rule. When an account runs hot, it skips or waits (cron) or *suggests* a switch (interactive), and never switches on its own. The only automatic fallback allowed is a pay-per-use backend (API key, Nous, Ollama) that the user has listed explicitly.
- **Separation helps the LL side too.** The LL org's server-managed settings and policy limits (`remote-settings.json`, `policy-limits.json` in `~/.claude`) are per config dir, so personal sessions stop inheriting LL's org policy and LL sessions keep it.

## What breaks in agentctl today with `CLAUDE_CONFIG_DIR`

agentctl hardcodes `~/.claude` in several places. With a second config dir these resolve to the wrong account:

| Where | Uses | Fix |
|---|---|---|
| `internal/claude/paths.go` `LogPath` | `~/.claude/projects/<slug>/<sid>.jsonl` | Take the config dir. Prefer the `transcript_path` from the hook payload (already recorded) |
| `internal/sessions/sessions.go` (projects root, `FindLatestLog*`, `FindLogBySessionIDIn`) | `~/.claude/projects` | Search every configured account's `projects/` (cost, recap, `agentctl cost`) |
| `internal/agent/agent.go` | `~/.claude/shared`, `~/.claude/agents` | Keep reading from a single *home* config dir (see "shared content" below) |
| `internal/hooks/shellprefix.go`, `hooks/session.go`, `dispatch/nous.go` (`~/.claude/settings.json`) | user settings | Resolve per account |
| `internal/statusline` → `ratelimit.PushedRateLimits` | one global rate-limit file | **Key it by account.** 5h/7d windows are per account, and mixing them makes the chrome bar, cost and the cron usage gate wrong |
| `internal/tokens` / `sessions.ClassifyBilling` | "sub" vs "metered" by model | Tag `sub:<account>` so `agentctl cost` splits LL vs personal |
| `windowstate`, `restore.json` | none | Add `account` so restore relaunches under the same login |
| Native `ListAgents` | each session registers in `$CLAUDE_CONFIG_DIR/sessions/<pid>.json` | **Unknown whether discovery crosses config dirs.** See the spike below |

### Decision: accounts do not talk to each other (2026-11-17)

Brendan wants hard separation: LL sessions and personal sessions must not message each other. agentctl therefore **never** bridges native messaging across accounts: no shared `sessions/` registry, and `sessions/` is never in the symlink allowlist. Whether Claude Code's own discovery happens to cross config dirs is not relied on. If it does, agentctl adds `crossSessionInbound` and `SendMessage`/`ListAgents` deny handling only if a leak shows up in practice.

Consequences:

- **Cross-account dispatch.** When the worker's resolved account differs from the dispatching window's, the worker preamble omits the "report to your orchestrator with SendMessage" section and the orchestrator `@N.%P` pointer. The worker finishes with its PR as usual, and the orchestrator learns about it from the existing `agentctl notice:` done/blocked backstop (agentctl's own mailbox, which is per-machine, not per-account) and the PR itself. The dispatch popup shows the resolved account.
- **The agentctl mailbox notices are allowed across accounts.** They are short factual status lines written by agentctl, not agent-to-agent messages.

## Design

### 1. Config (global only)

```json
{
  "accounts": {
    "ll":       {"config_dir": "~/.claude",          "label": "Lincoln Loop (Team)"},
    "personal": {"config_dir": "~/.claude-personal", "label": "Personal (Max)"}
  },
  "default_account": "ll",
  "account_rules": [
    {"path": "~/code/ll/**",       "account": "ll"},
    {"path": "~/code/personal/**", "account": "personal"},
    {"path": "~/code/foxden/**",   "account": "personal"}
  ]
}
```

Resolution order for a launch: `--account` flag > cron job `account` > `agents.<name>.account` > first matching `account_rules` path (longest-prefix wins, matched on the *repo root*, so worktrees inherit) > the dispatching window's account > `default_account`. Global-only, like `backends` and command-type keys: a cloned repo must not be able to route itself onto a paid account.

`backend` (Nous/Ollama) and `account` are orthogonal. `backend` swaps the API endpoint and key. `account` picks the Claude login for the Anthropic first-party case. When a backend is active, account is irrelevant to billing, but the config dir still determines settings and transcript location.

Keep `~/.claude` as `ll`, since it is already logged in there, has the history, and is where LL's managed settings landed. Add `~/.claude-personal`.

### 2. Shared content across accounts

Each config dir is a full Claude home. Agents, shared includes, user `CLAUDE.md`, user `settings.json` (hooks, statusline, permissions), skills, commands and agent memory should be the same in both, while credentials, `projects/`, `sessions/`, history, caches and org policy files stay separate.

`agentctl account add` symlinks a fixed allowlist from the home dir (`~/.claude`) into the new dir: `agents/`, `shared/`, `skills/`, `commands/`, `CLAUDE.md`, `settings.json`, `hooks/`, `plugins/` (verify plugins tolerate sharing). Agent memory (`agent-memory/`) is per account, because Claude Code only trusts its memory dir inside its own config dir. **Never** `.credentials.json`, `sessions/`, `projects/`, `remote-settings*`, `policy-limits*`, `.claude.json`, `history.jsonl`, `daemon*`, `statsig`/telemetry. It's an allowlist, not a denylist, so new Claude Code files default to private.

Caveat: Claude Code rewrites `settings.json` (e.g. `/config` toggles). Through a symlink that's what we want, since the edit lands in the shared file, provided Claude Code writes through the link rather than replacing it with a file. Verify. If it replaces the link, `account doctor` detects and repairs it.

### 3. CLI

```
agentctl account add personal --config-dir ~/.claude-personal
    # mkdir 0700, symlink the shared allowlist, then open a tmux popup running
    #   CLAUDE_CONFIG_DIR=~/.claude-personal claude auth login
    # (Anthropic's own flow; agentctl never sees the token)
agentctl account list        # name, dir, `claude auth status --json` (email, org, plan), 5h/7d %, live windows
agentctl account doctor      # symlinks intact, logged in, login expiring (<3d), stray credentials in env
agentctl account rm personal # unregister only; never deletes the dir
dispatch ... --account personal
```

`account list` reads identity via `CLAUDE_CONFIG_DIR=<dir> claude auth status --json`, which reports `email`, `orgName`, `subscriptionType`, `configDirectory` and `projectsDirectory` without agentctl reading any credential file.

### 4. Launching

`tmux.buildLaunchEnv` sets `CLAUDE_CONFIG_DIR` for the resolved account (absolute path) and records `account` in windowstate. It must also **unset** `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN` and `CLAUDE_CODE_OAUTH_TOKEN` inherited from the tmux server env unless a backend set them, because those outrank the per-dir login (auth precedence 2–5) and would silently bill the wrong place. This is the same class of bug as the AGENTCTL_RUNTIME_DIR env leak.

Under the multi-harness `Harness` interface this becomes `Harness.AccountEnv(account) map[string]string`. Codex has `CODEX_HOME` and pi has its own agent dir, so "account = home dir for that harness" generalizes. Accounts are therefore configured per harness: `accounts.<name>.homes: {claude: "~/.claude-personal", codex: "~/.codex-personal"}`. v1 ships Claude only with the flat `config_dir` field and migrates later.

### 5. UI

- Chrome bar / hub row: a short account tag (`LL` / `me`) next to the model when more than one account is configured, plus that account's 5h/7d meter instead of a global one.
- `agentctl cost` groups by account.
- Phone dispatch screen: account picker next to backend/model/effort (#84 pattern).

### 6. Cron

Jobs get `account`, resolved by the same rules. The usage gate reads *that account's* 5h/7d. When it's hot the run is skipped (logged as `skipped:usage:<account>`), never moved to another subscription. A job can list `fallback_backend` for a pay-per-use backend.

## Rollout

1. **Manual check during PR 2** (no code): symlinked `settings.json` survives a `/config` write (Claude Code writes through the link instead of replacing it).
2. **Path plumbing, no behavior change:** a `claudehome` resolver (config dir for an account; default `~/.claude` or `$CLAUDE_CONFIG_DIR`) replacing the hardcoded paths in the table; rate limits keyed by account; `account` in windowstate and restore.
3. **Accounts config + `account add/list/doctor` + `dispatch --account` + rules + env scrubbing.**
4. **UI tags, cost grouping, phone picker, cron `account`.**
5. Fold into `Harness` when multi-harness phase 1 lands.

## Tests worth writing

- Resolution order table (flag, job, agent, rule longest-prefix on repo root via worktree path, parent window, default).
- Project-local config with `accounts`/`account_rules` is ignored.
- Launch env sets an absolute `CLAUDE_CONFIG_DIR` and strips inherited `ANTHROPIC_API_KEY`/`ANTHROPIC_AUTH_TOKEN`/`CLAUDE_CODE_OAUTH_TOKEN` unless the backend set them.
- `account add` symlinks exactly the allowlist and never creates or links `.credentials.json` (golden dir listing).
- Rate-limit store: two accounts don't overwrite each other.
- No code path opens `.credentials.json` or calls `security find-generic-password` (grep test).

## Open questions

1. Defaults chosen: `brandon` (orchestrator) resolves to personal via the `~/code/personal/**` rule.
2. Any LL repos outside `~/code/ll/`, e.g. client repos under other paths, that need rules?
3. Decided: a cross-account dispatch shows the account in the popup, with no second prompt.
