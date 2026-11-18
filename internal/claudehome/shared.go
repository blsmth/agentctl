package claudehome

// SharedEntries is the allowlist of entries `agentctl account add` symlinks
// from HomeDir() into a new account dir, so agents, includes, settings and
// skills stay identical across accounts.
//
// It is an allowlist, not a denylist: anything Claude Code adds later stays
// private to each dir. Credentials, projects/, history, caches, org policy
// files and sessions/ are never shared. sessions/ in particular must never be
// linked: accounts are isolated on purpose and do not discover or message
// each other.
var SharedEntries = []string{
	"agents",
	"shared",
	"skills",
	"commands",
	"CLAUDE.md",
	"settings.json",
	"hooks",
	"plugins",
}

// NeverShared names entries that must never be a symlink in an account dir
// (`account doctor` reports one that is).
//
// agent-memory is here because Claude Code auto-allows writes to its memory
// dir only when the real path is inside the current CLAUDE_CONFIG_DIR. Through
// a symlink to ~/.claude the write resolves outside it and Claude Code prompts
// on every memory write. Memory must be a real dir inside the account.
var NeverShared = []string{"sessions", "projects", "agent-memory"}
