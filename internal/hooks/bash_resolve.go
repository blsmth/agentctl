package hooks

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
)

// fallbackBash is the interpreter used when no candidate resolves to a
// modern (major >= 4) bash. It is guaranteed to exist on every macOS and
// Linux install we support.
const fallbackBash = "/bin/bash"

// bashCandidates lists the paths checked, in order, when resolving a modern
// bash for shell-prefix.sh's shebang. The first candidate whose major
// version is >= 4 wins.
//
//   - /opt/homebrew/bin/bash covers Apple Silicon Homebrew installs.
//   - /usr/local/bin/bash covers Intel Homebrew and the common Linux
//     location for a manually-installed bash.
//   - "bash" (resolved via exec.LookPath) covers Nix, asdf, and most Linux
//     distro package managers, wherever PATH happens to point.
//   - /bin/bash is the guaranteed fallback -- it exists everywhere, even if
//     (on macOS) it is the 2007-vintage bash 3.2.
var bashCandidates = []string{
	"/opt/homebrew/bin/bash",
	"/usr/local/bin/bash",
	"", // sentinel: resolved via exec.LookPath("bash") at call time
	fallbackBash,
}

var bashVersionLineRE = regexp.MustCompile(`version (\d+)\.`)

// probeBashVersion shells out to "<path> --version" and parses the major
// version number out of the first line, e.g.
// "GNU bash, version 5.3.15(1)-release (aarch64-apple-darwin24.6.0)" -> 5.
// It is defensive by design: any failure (missing binary, timeout,
// unparseable output) reports ok=false rather than panicking, since this
// runs against an arbitrary binary on the user's machine.
func probeBashVersion(path string) (major int, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, path, "--version").Output() // fixed set of vetted candidate paths
	if err != nil {
		return 0, false
	}
	return parseBashMajorVersion(string(out))
}

// parseBashMajorVersion extracts the major version number from bash
// --version output. It is a pure function so the parsing logic can be
// tested against real and garbage strings without shelling out.
func parseBashMajorVersion(versionOutput string) (major int, ok bool) {
	m := bashVersionLineRE.FindStringSubmatch(versionOutput)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// resolveBash picks the first candidate whose probe reports a major version
// >= 4, falling back to /bin/bash if none qualify (including an empty
// candidate list). Candidates that don't exist, aren't executable, or fail
// the probe are skipped silently -- the probe is the single source of truth
// for "does this work", so resolveBash does not separately stat the file.
//
// probe is injectable so tests can exercise the selection logic without
// depending on what bash binaries happen to be installed on the machine
// running the test.
func resolveBash(candidates []string, probe func(string) (major int, ok bool)) string {
	seen := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		major, ok := probe(c)
		if !ok || major < 4 {
			continue
		}
		return c
	}
	return fallbackBash
}

var (
	resolveBashOnce  sync.Once
	resolvedBashPath string
)

// resolvedBash resolves and caches the bash interpreter to use for
// shell-prefix.sh's shebang. The resolution (three subprocess execs in the
// worst case) happens once per process via sync.Once -- EnsureShellPrefixScript
// runs on every dispatch tick, and repeating `bash --version` probes on every
// call would be wasteful. This means a bash installed mid-session is not
// picked up until agentctl restarts; that's an acceptable trade for avoiding
// per-dispatch subprocess overhead.
func resolvedBash() string {
	resolveBashOnce.Do(func() {
		candidates := make([]string, len(bashCandidates))
		copy(candidates, bashCandidates)
		// Resolve the PATH-based candidate (the "" sentinel) once, up front.
		for i, c := range candidates {
			if c == "" {
				if p, err := exec.LookPath("bash"); err == nil {
					candidates[i] = p
				}
			}
		}

		path := resolveBash(candidates, probeBashVersion)
		major, _ := probeBashVersion(path)
		resolvedBashPath = path

		if major >= 4 {
			logging.Log("shell-prefix: using bash %d at %s", major, path)
		} else {
			logging.Warn("shell-prefix: falling back to bash %d at %s -- a modern bash (>= 4) avoids a heredoc-in-command-substitution parse bug in bash 3.2", major, path)
		}
	})
	return resolvedBashPath
}

// resetResolvedBashForTest clears the cached resolution so tests can force
// resolvedBash() to re-run. Not used outside tests.
func resetResolvedBashForTest() {
	resolveBashOnce = sync.Once{}
	resolvedBashPath = ""
}
