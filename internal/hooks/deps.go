package hooks

import (
	"os"
	"os/exec"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/mailbox"
	"github.com/blsmth/agentctl/internal/notify"
	"github.com/blsmth/agentctl/internal/tmux"
)

// Deps holds injectable external dependencies for hook handlers.
// Production code uses DefaultDeps(). Tests inject stubs via SetDepsForTest.
type Deps struct {
	// RunCommand executes an external command and returns its combined output.
	// Wraps exec.Command(name, args...).CombinedOutput().
	RunCommand func(name string, args ...string) ([]byte, error)

	// TmuxDisplay resolves a tmux format string for a target pane/window.
	// Wraps: tmux display-message -t <target> -p <format>
	TmuxDisplay func(target, format string) string

	// LoadConfig returns the current agentctl config.
	LoadConfig func() *config.Config

	// LoadProjectConfig returns project config for the given directory.
	LoadProjectConfig func(dir string) *config.ProjectConfig

	// Notify sends a desktop notification. opts carries the title/subtitle/
	// message plus optional env vars for the command.
	Notify func(opts notify.Options)

	// StatFile checks whether a file exists and returns its info.
	// Wraps os.Stat. Used by CheckDone to detect go.mod without requiring
	// the test process to run from a real Go project directory.
	StatFile func(name string) (os.FileInfo, error)

	// EnqueueMailboxMessage durably writes a mailbox message (see
	// docs/agent-messaging-design.md §4). Wraps mailbox.Enqueue. Injectable
	// so tests can assert on what CheckDone/Notify would have sent, or force
	// an enqueue failure, without touching the real filesystem.
	EnqueueMailboxMessage func(m mailbox.Message) (string, error)
}

// DefaultDeps returns the production dependency set.
func DefaultDeps() *Deps {
	return &Deps{
		RunCommand:            defaultRunCommand,
		TmuxDisplay:           tmux.DisplayTarget,
		LoadConfig:            config.Load,
		LoadProjectConfig:     config.LoadProject,
		Notify:                hookNotify,
		StatFile:              os.Stat,
		EnqueueMailboxMessage: mailbox.Enqueue,
	}
}

// defaultRunCommand is the production implementation of Deps.RunCommand.
func defaultRunCommand(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// deps is the active dependency set. Replaced in tests via SetDepsForTest.
var deps = DefaultDeps()

// SetDepsForTest overrides the package-level deps for the duration of a test.
// Call t.Cleanup(func() { hooks.SetDepsForTest(hooks.DefaultDeps()) }) to restore.
func SetDepsForTest(d *Deps) {
	deps = d
}
