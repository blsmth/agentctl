// Package cmd implements agentctl's subcommands. The Kong CLI struct in the
// root package (cli.go) only parses arguments; each command's Run method
// calls straight into a function here -- RunDispatch, RunCleanup, RunStatus,
// CmdChromeBar, and so on -- which does the actual work.
//
// The package covers the whole command surface: session lifecycle (start,
// quit, restart, restore), agent windows (launch, kill, logs, dispatch,
// cleanup), the full-screen and popup TUIs (agents, skills, memory, sessions,
// tickets, settings, the chrome bar and HUD), cross-session views (hub,
// status, peek, whoami), messaging (send, inbox, wake), the context store,
// audit and usage reports, and setup (init, new, sync, hooks and template
// installation).
//
// Most functions read their dependencies (config.Load, tmux, windowstate)
// directly and report errors to stderr themselves, since they are leaf
// entry points of a CLI process. [App] bundles the config, repo directory,
// and tmux session name for the commands that want them resolved once.
package cmd
