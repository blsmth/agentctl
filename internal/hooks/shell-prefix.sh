#!/bin/bash
# The shebang above is a placeholder overwritten at write time by
# ShellPrefixScriptContent/withResolvedShebang (internal/hooks/shellprefix.go,
# internal/hooks/bash_resolve.go) with an absolute path to a resolved modern
# bash. It is deliberately NOT "#!/usr/bin/env bash": PATH order varies
# between login shells, non-interactive shells, and tmux-launched processes,
# so "env bash" cannot be trusted to pick a modern interpreter -- on macOS it
# commonly still resolves to /bin/bash if that happens to precede a Homebrew
# bash on PATH. Resolving the absolute path once, at write time, removes that
# dependency entirely.
#
# Why it matters: macOS ships bash 3.2 (2007) at /bin/bash, whose parser
# mishandles a quoted-delimiter heredoc nested inside command substitution
# when the heredoc body contains an apostrophe (fails with "unexpected EOF
# while looking for matching '"). If no bash >= 4 can be resolved, this falls
# back to /bin/bash (byte-identical behavior to before); it only changes
# anything -- for the better -- once a modern bash (e.g. Homebrew's bash 5.x)
# is found. Do NOT hand-edit this shebang; it is regenerated on every write.
# agentctl shell prefix — wraps every Bash tool invocation
# AUTO-MANAGED by agentctl — do not edit by hand

AGENTCTL_HOOKS_DIR="${HOME}/.claude/agents/agentctl/shell-hooks.d"
AGENTCTL_ENV_DIR="${HOME}/.claude/agents/agentctl/env.d"
AGENT="${AGENTCTL_AGENT_NAME:-unknown}"

# Resolve the agentctl binary — prefer the env var, fall back to PATH.
# Exported (via sourcing, not `export`) for shell-hooks.d scripts below,
# which are sourced into this shell and can call "$_agentctl" themselves.
# NOT dead code despite having no in-repo caller: shell-hooks.d scripts are
# user-authored and sourced into this shell, so this variable is an extension
# point, and removing it would silently break any hook that uses it. This was
# decided deliberately when the last in-repo caller was removed.
_agentctl="${AGENTCTL_BIN:-agentctl}"

# 1. Source per-agent env overrides if they exist
[ -f "${AGENTCTL_ENV_DIR}/${AGENT}.env" ] && source "${AGENTCTL_ENV_DIR}/${AGENT}.env"

# 2. Run any user-defined pre-exec hooks (sorted order)
if [ -d "$AGENTCTL_HOOKS_DIR" ]; then
  for hook in "$AGENTCTL_HOOKS_DIR"/*.sh; do
    [ -x "$hook" ] && source "$hook" "$@"
    # If a hook sets AGENTCTL_BLOCK=1, abort execution
    if [ "${AGENTCTL_BLOCK:-0}" = "1" ]; then
      echo "blocked by $(basename "$hook"): ${AGENTCTL_BLOCK_REASON:-no reason given}" >&2
      exit 1
    fi
  done
fi

# 3. Run the actual command and propagate its exit code.
#    Claude Code passes the entire shell command as a single string argument,
#    so we eval it rather than forking a subprocess, ensuring sourced
#    env.d/shell-hooks.d state from above actually affects the executed command.
#    State transitions (working/idle/blocked) are driven by the PreToolUse and
#    PostToolUse hooks registered in the Claude Code settings file — no signal
#    calls are needed here.
eval "$@"
_agentctl_rc=$?

exit $_agentctl_rc
