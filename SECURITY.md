# Security Policy

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub's private vulnerability reporting: open the repository's **Security** tab and choose **Report a vulnerability** (a GitHub security advisory). Do not open a public issue or pull request for a suspected vulnerability.

Include what you found, how to reproduce it, and the agentctl version (`agentctl version`). You can expect an acknowledgement within a few days. Fixes are released as a new version and credited in the advisory unless you prefer otherwise.

## Supported versions

Only the latest release receives security fixes.

## What to know about agentctl's trust model

agentctl is a local developer tool that runs commands on your behalf, so treat its configuration like code:

- It installs Claude Code **hooks** that run the agentctl binary on every tool call and session event.
- It runs **shell commands** from configuration: `notify_cmd`, `hooks.*`, and `dispatch.on_complete`. To keep a cloned repository from executing commands, these keys are honored **only** from the global config (`~/.config/agentctl/config.json`, or `$XDG_CONFIG_HOME/agentctl/`), never from a project-local `.agentctl/config.json`. See [docs/configuration.md](docs/configuration.md).
- Dispatched agents run Claude Code with the permissions you configure, in a git worktree. Settings such as `auto_accept` remove the human approval step; use them deliberately.
- It calls `gh`, `git`, and `tmux` using your existing credentials and sessions.

Reports about ways a project-local file, issue body, or other untrusted input can cause unintended command execution or approval of a tool call are in scope.
