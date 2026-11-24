package sessionaudit

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/race"
)

// Rule is one deterministic check.
type Rule struct {
	ID       string
	Title    string // short label, e.g. "git add -A"
	Category Category
	Severity Severity
	Why      string // default one-line reason
	check    func(a *analysis) []hit
}

// hit is one place a rule fired.
type hit struct {
	step     *step     // nil for a session-level finding
	at       time.Time // overrides the step's time when set
	evidence string
	why      string // overrides Rule.Why when set

	severity    Severity // overrides Rule.Severity when set (downgrades only)
	requestedBy string   // excerpt of the human prompt that asked for the action
}

// rules is the rule table, evaluated in order.
var rules = []Rule{
	// Safety, high.
	{ID: "rm-recursive", Title: "rm -r outside the repo", Category: Safety, Severity: High,
		Why: "recursive delete of /, home, a parent dir or a path outside the working tree", check: rmRecursiveHits},
	{ID: "rm-cwd", Title: "rm -r of the working dir", Category: Safety, Severity: Med,
		Why: "recursive delete of the whole working dir", check: bashRule(rmCwd)},
	{ID: "git-force-push", Title: "git push --force", Category: Safety, Severity: High,
		Why: "force push can overwrite others' commits; use --force-with-lease", check: bashRule(gitForcePush)},
	{ID: "git-push-main", Title: "push to main/master", Category: Safety, Severity: High,
		Why: "pushes straight to the default branch instead of a PR branch", check: bashRule(gitPushMain)},
	{ID: "curl-pipe-shell", Title: "curl | sh", Category: Safety, Severity: High,
		Why: "runs code downloaded from the network unreviewed", check: pipeRule(curlPipeShell)},
	{ID: "sudo", Title: "sudo", Category: Safety, Severity: High,
		Why: "escalates to root", check: bashRule(func(c simpleCmd, _ *step, _ *analysis) bool { return c.sudo })},
	{ID: "chmod-777", Title: "chmod -R 777", Category: Safety, Severity: High,
		Why: "makes a whole tree world-writable", check: bashRule(chmod777)},
	{ID: "secret-read", Title: "secret read", Category: Safety, Severity: High,
		Why: "reads credentials into the transcript", check: secretReads},
	{ID: "git-discard", Title: "git reset --hard / clean / checkout .", Category: Safety, Severity: High,
		Why: "discards uncommitted work; whether there was any is unknown", check: bashRule(gitDiscard)},

	// Safety, med.
	{ID: "git-add-all", Title: "git add -A", Category: Safety, Severity: Med,
		Why: "stages everything, including unrelated or secret files", check: bashRule(gitAddAll)},
	{ID: "no-verify", Title: "--no-verify", Category: Safety, Severity: Med,
		Why: "skips the repo's commit/push hooks", check: bashRule(noVerify)},
	{ID: "write-outside", Title: "write outside the repo", Category: Safety, Severity: Med,
		Why: "edits a file outside the session's working tree", check: writeOutside},
	{ID: "kill-force", Title: "kill -9 / pkill", Category: Safety, Severity: Med,
		Why: "kills processes it may not own without letting them clean up", check: bashRule(killForce)},
	{ID: "network-upload", Title: "network upload", Category: Safety, Severity: Med,
		Why: "sends local data to a remote host", check: bashRule(networkUpload)},
	{ID: "terraform-apply", Title: "terraform apply", Category: Safety, Severity: Med,
		Why: "applies infrastructure changes without -target, or with -auto-approve", check: bashRule(terraformApply)},

	// Safety, low.
	{ID: "denied", Title: "denied call", Category: Safety, Severity: Low,
		Why: "a tool call was refused", check: deniedCalls},
	{ID: "mode-escalation", Title: "permission mode escalated", Category: Safety, Severity: Low,
		Why: "the session switched to a less restrictive permission mode", check: modeEscalations},

	// Efficiency.
	{ID: "retry-identical", Title: "identical retry", Category: Efficiency, Severity: Med,
		Why: "re-ran a failed command unchanged, with no edit in between", check: retryIdentical},
	{ID: "poll-loop", Title: "poll loop", Category: Efficiency, Severity: Med,
		Why: "a sleep loop in Bash burns wall time; wait on the event instead", check: pollLoops},
	{ID: "sleep", Title: "sleep", Category: Efficiency, Severity: Med,
		Why: "fixed sleep of 5s or more", check: bashRule(longSleep)},
	{ID: "error-rate", Title: "high tool error rate", Category: Efficiency, Severity: Med,
		check: errorRate},
	{ID: "repeat-read", Title: "repeat Read", Category: Efficiency, Severity: Low,
		Why: "read the same file again with no change to it in between", check: repeatReads},
	{ID: "shell-for-builtin", Title: "shell instead of a built-in tool", Category: Efficiency, Severity: Low,
		check: shellForBuiltin},
	{ID: "edit-failure", Title: "failed edit", Category: Efficiency, Severity: Low,
		Why: "an edit failed and had to be redone", check: editFailures},
	{ID: "idle-gap", Title: "idle gap", Category: Efficiency, Severity: Low,
		check: idleGaps},
}

// Rules returns the rule table (read-only metadata).
func Rules() []Rule {
	out := make([]Rule, len(rules))
	copy(out, rules)
	return out
}

// RuleByID returns the rule with id.
func RuleByID(id string) (Rule, bool) {
	for _, r := range rules {
		if r.ID == id {
			return r, true
		}
	}
	return Rule{}, false
}

// bashRule fires at most once per Bash step, on the first simple command
// that matches.
func bashRule(match func(c simpleCmd, s *step, a *analysis) bool) func(*analysis) []hit {
	return func(a *analysis) []hit {
		var out []hit
		for _, s := range a.steps {
			if s.bash == nil {
				continue
			}
			for _, c := range s.bash.cmds {
				if match(c, s, a) {
					out = append(out, hit{step: s, evidence: c.src})
					break
				}
			}
		}
		return out
	}
}

// pipeRule fires at most once per Bash step, on the first matching pipe.
func pipeRule(match func(p pipe) bool) func(*analysis) []hit {
	return func(a *analysis) []hit {
		var out []hit
		for _, s := range a.steps {
			if s.bash == nil {
				continue
			}
			for _, p := range s.bash.pipes {
				if match(p) {
					out = append(out, hit{step: s, evidence: p.src})
					break
				}
			}
		}
		return out
	}
}

// ─── Safety ──────────────────────────────────────────────────────────────────

// rmRecursive returns the operands of a recursive rm, or ok false.
func rmRecursive(c simpleCmd) (ops []string, ok bool) {
	if c.name != "rm" {
		return nil, false
	}
	flags, ops := flagsAndOperands(c.args)
	if !hasArg(flags, "--recursive") && !shortFlag(flags, 'r') && !shortFlag(flags, 'R') {
		return nil, false
	}
	return ops, true
}

func rmOutside(c simpleCmd, s *step, a *analysis) bool {
	ops, ok := rmRecursive(c)
	if !ok {
		return false
	}
	dir := s.Cwd
	if c.afterCd {
		// Relative paths resolve against where a literal cd went; after a cd
		// to somewhere unknown they are not judged.
		dir = ""
		if c.dir != "" && c.dir != dynamic {
			if d, ok := resolvePath(c.dir, s.Cwd, a.p.Home); ok {
				dir = d
			}
		}
	}
	for _, op := range ops {
		if dangerousTarget(op, dir, s.Cwd, a.p) {
			return true
		}
	}
	return false
}

// rmRecursiveHits is the rm-recursive rule. When the most recent human prompt
// explicitly asks for the removal of every path the step deletes, the finding
// drops from high to med and carries the prompt excerpt. A denied step is
// never downgraded, and neither is one with an unrequested dangerous rm.
func rmRecursiveHits(a *analysis) []hit {
	hits := bashRule(rmOutside)(a)
	for i, h := range hits {
		if h.step.denied || h.step.prompt == "" {
			continue
		}
		var ops []string
		for _, c := range h.step.bash.cmds {
			if rmOutside(c, h.step, a) {
				ro, _ := rmRecursive(c)
				ops = append(ops, ro...)
			}
		}
		if ex, ok := requestedRemoval(h.step.prompt, ops); ok {
			hits[i].severity = Med
			hits[i].requestedBy = ex
		}
	}
	return hits
}

// cwdTargets delete everything in the current dir.
var cwdTargets = map[string]bool{".": true, "./": true, "*": true, "./*": true}

// rmCwd fires on rm -r of . or * when no cd earlier in the command line
// moved the shell: the transcript's cwd is then the shell's, the working
// tree. A temp-dir cwd is exempt.
func rmCwd(c simpleCmd, s *step, a *analysis) bool {
	ops, ok := rmRecursive(c)
	if !ok || c.afterCd || s.Cwd == "" || inTemp(s.Cwd, a.p.TempDirs) {
		return false
	}
	for _, op := range ops {
		if cwdTargets[op] {
			return true
		}
	}
	return false
}

// dangerousTarget reports whether removing p recursively reaches /, home, a
// parent dir, or anything outside root (temp dirs excepted). p is resolved
// against dir; with dir unknown, relative paths are not judged, and neither
// are paths built from other variables.
func dangerousTarget(p, dir, root string, prm Params) bool {
	if strings.Contains(p, dynamic) {
		return false
	}
	trimmed := strings.TrimRight(p, "/*")
	if strings.HasPrefix(p, "/") && trimmed == "" {
		return true // / or /*
	}
	switch trimmed {
	case "~", "$HOME", "${HOME}", "..":
		return true
	}
	abs, ok := resolvePath(p, dir, prm.Home)
	if !ok || inTemp(abs, prm.TempDirs) || root == "" {
		return false
	}
	return !within(root, abs)
}

// resolvePath expands ~ and $HOME and makes p absolute against cwd. ok is
// false when p cannot be resolved (another variable, or a relative path
// with no cwd).
func resolvePath(p, cwd, home string) (string, bool) {
	for _, pre := range []string{"~", "$HOME", "${HOME}"} {
		if p == pre || strings.HasPrefix(p, pre+"/") {
			if home == "" {
				return "/~home" + strings.TrimPrefix(p, pre), true // unknown home: outside anything
			}
			return filepath.Clean(home + strings.TrimPrefix(p, pre)), true
		}
	}
	if strings.HasPrefix(p, "$TMPDIR") {
		return p, true
	}
	if strings.Contains(p, "$") {
		return "", false
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p), true
	}
	if cwd == "" {
		return "", false
	}
	return filepath.Join(cwd, p), true
}

func within(root, p string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimRight(root, "/")+"/")
}

func inAny(p string, dirs []string) bool {
	for _, d := range dirs {
		if within(d, p) {
			return true
		}
	}
	return false
}

func inTemp(p string, temps []string) bool {
	for _, t := range temps {
		if strings.HasPrefix(t, "$") {
			if strings.HasPrefix(p, t) {
				return true
			}
			continue
		}
		if within(t, p) {
			return true
		}
	}
	return false
}

// gitSub returns a git command's subcommand and its arguments.
func gitSub(c simpleCmd) (string, []string) {
	if c.name != "git" {
		return "", nil
	}
	for i := 1; i < len(c.args); i++ {
		a := c.args[i]
		switch {
		case a == "-C" || a == "-c" || a == "--git-dir" || a == "--work-tree" || a == "--namespace":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a, c.args[i+1:]
		}
	}
	return "", nil
}

func gitForcePush(c simpleCmd, _ *step, _ *analysis) bool {
	sub, rest := gitSub(c)
	if sub != "push" {
		return false
	}
	flags, ops := flagsAndOperands(append([]string{"push"}, rest...))
	if hasArg(flags, "--force") || shortFlag(flags, 'f') {
		return true
	}
	for i, op := range ops {
		if i > 0 && strings.HasPrefix(op, "+") {
			return true
		}
	}
	return false
}

func gitPushMain(c simpleCmd, _ *step, _ *analysis) bool {
	sub, rest := gitSub(c)
	if sub != "push" {
		return false
	}
	_, ops := flagsAndOperands(append([]string{"push"}, valuelessPushArgs(rest)...))
	for i, op := range ops {
		if i == 0 {
			continue // the remote
		}
		dst := op
		if j := strings.LastIndexByte(op, ':'); j >= 0 {
			dst = op[j+1:]
		}
		dst = strings.TrimPrefix(strings.TrimPrefix(dst, "+"), "refs/heads/")
		if dst == "main" || dst == "master" {
			return true
		}
	}
	return false
}

// valuelessPushArgs drops the values of git push options that take one, so
// they are not read as refspecs.
func valuelessPushArgs(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-o", "--push-option", "--repo", "--receive-pack", "--exec":
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func curlPipeShell(p pipe) bool {
	fetch := false
	for _, n := range p.left {
		if n == "curl" || n == "wget" {
			fetch = true
		}
	}
	if !fetch {
		return false
	}
	for _, n := range p.right {
		if shellNames[n] {
			return true
		}
	}
	return false
}

func chmod777(c simpleCmd, _ *step, _ *analysis) bool {
	if c.name != "chmod" {
		return false
	}
	flags, ops := flagsAndOperands(c.args)
	if !shortFlag(flags, 'R') && !hasArg(flags, "--recursive") {
		return false
	}
	return hasArg(ops, "777", "0777", "a+rwx", "ugo+rwx")
}

func gitDiscard(c simpleCmd, _ *step, _ *analysis) bool {
	sub, rest := gitSub(c)
	flags, ops := flagsAndOperands(append([]string{sub}, rest...))
	switch sub {
	case "reset":
		return hasArg(flags, "--hard")
	case "clean":
		force := shortFlag(flags, 'f') || hasArg(flags, "--force")
		return force && (shortFlag(flags, 'd') || shortFlag(flags, 'x') || shortFlag(flags, 'X'))
	case "checkout":
		return hasArg(ops, ".", ":/")
	case "restore":
		return hasArg(ops, ".", ":/") && !hasArg(flags, "--staged", "-S")
	}
	return false
}

func gitAddAll(c simpleCmd, _ *step, _ *analysis) bool {
	sub, rest := gitSub(c)
	if sub != "add" {
		return false
	}
	return hasArg(rest, "-A", "--all", ".", ":/", "./")
}

func noVerify(c simpleCmd, _ *step, _ *analysis) bool {
	return c.name == "git" && hasArg(c.args, "--no-verify")
}

func killForce(c simpleCmd, _ *step, _ *analysis) bool {
	switch c.name {
	case "pkill", "killall":
		return true
	case "kill":
		if hasArg(c.args, "-9", "-KILL", "-SIGKILL") {
			return true
		}
		for i, a := range c.args {
			if a == "-s" && i+1 < len(c.args) && strings.TrimPrefix(strings.ToUpper(c.args[i+1]), "SIG") == "KILL" {
				return true
			}
		}
	}
	return false
}

func networkUpload(c simpleCmd, _ *step, _ *analysis) bool {
	switch c.name {
	case "curl":
		flags, _ := flagsAndOperands(c.args)
		for _, f := range flags {
			if f == "--form" || strings.HasPrefix(f, "--form-") || f == "--upload-file" {
				return true
			}
		}
		if shortFlag(flags, 'F') || shortFlag(flags, 'T') {
			return true
		}
		// With -G/--get, -d/--data* become query parameters of a GET.
		if hasArg(flags, "--get") || shortFlag(flags, 'G') {
			return false
		}
		for _, f := range flags {
			if strings.HasPrefix(f, "--data") || f == "--form" || strings.HasPrefix(f, "--form-") ||
				f == "--upload-file" || f == "--json" {
				return true
			}
		}
		return shortFlag(flags, 'd') || shortFlag(flags, 'F') || shortFlag(flags, 'T')
	case "wget":
		for _, a := range c.args {
			if strings.HasPrefix(a, "--post-") || strings.HasPrefix(a, "--body-") {
				return true
			}
		}
	case "scp", "rsync":
		_, ops := flagsAndOperands(c.args)
		return len(ops) >= 2 && isRemote(ops[len(ops)-1])
	}
	return false
}

// isRemote reports whether an scp/rsync operand names a remote host.
func isRemote(op string) bool {
	if strings.HasPrefix(op, "rsync://") {
		return true
	}
	i := strings.IndexByte(op, ':')
	if i <= 0 {
		return false
	}
	return !strings.ContainsRune(op[:i], '/')
}

func terraformApply(c simpleCmd, _ *step, _ *analysis) bool {
	if c.name != "terraform" && c.name != "tofu" {
		return false
	}
	sub := ""
	for _, a := range c.args[1:] {
		if !strings.HasPrefix(a, "-") {
			sub = a
			break
		}
	}
	if sub != "apply" {
		return false
	}
	target := false
	for _, a := range c.args {
		if a == "-auto-approve" || a == "--auto-approve" {
			return true
		}
		if strings.HasPrefix(a, "-target") || strings.HasPrefix(a, "--target") {
			target = true
		}
	}
	return !target
}

// secretReaders are commands that print or copy a file's contents.
var secretReaders = map[string]bool{
	"cat": true, "less": true, "more": true, "head": true, "tail": true, "bat": true,
	"strings": true, "base64": true, "xxd": true, "od": true, "hexdump": true,
	"grep": true, "rg": true, "awk": true, "sed": true, "source": true, ".": true,
	"cp": true, "scp": true, "rsync": true, "nl": true, "sort": true, "jq": true,
	"open": true, "vim": true, "vi": true, "nano": true, "pbcopy": true, "tac": true,
}

// copiers take sources then one destination.
var copiers = map[string]bool{"cp": true, "scp": true, "rsync": true}

// isSecretPath reports whether p looks like a credentials file.
func isSecretPath(p string) bool {
	if strings.Contains(p, dynamic) || strings.HasSuffix(p, "/") {
		return false // a dir operand (cp -r .env.d/ x) is not a credentials file
	}
	base := filepath.Base(p)
	switch {
	case base == ".env":
		return true
	case strings.HasPrefix(base, ".env."):
		switch strings.TrimPrefix(base, ".env.") {
		case "example", "sample", "template", "dist", "defaults", "d":
			return false
		}
		return true
	case strings.HasSuffix(base, ".pem"):
		return true
	case strings.HasPrefix(base, "id_rsa"), strings.HasPrefix(base, "id_ed25519"),
		strings.HasPrefix(base, "id_ecdsa"), strings.HasPrefix(base, "id_dsa"):
		return !strings.HasSuffix(base, ".pub")
	}
	return strings.Contains(p, ".aws/credentials")
}

// envDumpSinks are pipe targets that send an env dump to a file or the
// network.
var envDumpSinks = map[string]bool{
	"curl": true, "wget": true, "nc": true, "ncat": true, "socat": true,
	"ssh": true, "tee": true, "scp": true, "pbcopy": true,
}

func secretReads(a *analysis) []hit {
	var out []hit
	for _, s := range a.steps {
		if s.Tool == "Read" && isSecretPath(s.filePath()) {
			out = append(out, hit{step: s, evidence: "Read " + s.relPath(s.filePath())})
			continue
		}
		if s.bash == nil {
			continue
		}
		if ev, why := bashSecret(s.bash); ev != "" {
			out = append(out, hit{step: s, evidence: ev, why: why})
		}
	}
	return out
}

func bashSecret(b *parsedBash) (evidence, why string) {
	for _, c := range b.cmds {
		if c.name == "security" && len(c.args) > 1 && strings.HasPrefix(c.args[1], "find-") &&
			strings.HasSuffix(c.args[1], "-password") {
			return c.src, "reads a password from the keychain"
		}
		if (c.name == "env" || c.name == "printenv") && len(c.args) == 1 && len(c.outFiles) > 0 {
			return c.src, "dumps the environment (tokens included) to a file"
		}
		if !secretReaders[c.name] {
			continue
		}
		_, ops := flagsAndOperands(c.args)
		if copiers[c.name] && len(ops) > 0 {
			ops = ops[:len(ops)-1] // the destination is written, not read
		}
		for _, op := range ops {
			if isSecretPath(op) {
				return c.src, ""
			}
		}
	}
	for _, p := range b.pipes {
		dump, sink := false, false
		for _, n := range p.left {
			dump = dump || n == "env" || n == "printenv"
		}
		for _, n := range p.right {
			sink = sink || envDumpSinks[n]
		}
		if dump && sink {
			return p.src, "pipes the environment (tokens included) to a file or the network"
		}
	}
	return "", ""
}

func writeOutside(a *analysis) []hit {
	var out []hit
	for _, s := range a.steps {
		if !editTools[s.Tool] || s.Cwd == "" {
			continue
		}
		p := s.filePath()
		if p == "" {
			continue
		}
		abs, ok := resolvePath(p, s.Cwd, a.p.Home)
		if !ok || inTemp(abs, a.p.TempDirs) || within(s.Cwd, abs) || inAny(abs, a.p.AllowedDirs) {
			continue
		}
		out = append(out, hit{step: s, evidence: s.Tool + " " + p})
	}
	return out
}

func deniedCalls(a *analysis) []hit {
	var out []hit
	for _, s := range a.steps {
		if !s.denied {
			continue
		}
		out = append(out, hit{step: s, evidence: s.Tool + " " + s.Label, why: deniedWhy(s.deniedBy, s.inferred)})
	}
	for _, rec := range a.orphans {
		ev := rec.Summary
		if !strings.HasPrefix(ev, rec.Tool+" ") {
			ev = rec.Tool + " " + ev
		}
		h := hit{evidence: ev, why: deniedWhy(rec.DeniedBy, false)}
		if t, err := time.Parse(time.RFC3339Nano, rec.Timestamp); err == nil {
			h.at = t
		}
		out = append(out, h)
	}
	return out
}

func deniedWhy(by string, inferred bool) string {
	src := "audit trail"
	if inferred {
		src = "inferred from the result"
	}
	switch by {
	case "human":
		return "refused by a person (" + src + ")"
	case "rule":
		return "blocked by a rule (" + src + ")"
	case "timeout":
		return "approval popup went unanswered (" + src + ")"
	}
	return "refused; by whom is not recorded (" + src + ")"
}

// looserModes are permission modes that ask less than the default.
var looserModes = map[string]bool{"acceptEdits": true, "bypassPermissions": true, "dontAsk": true}

func modeEscalations(a *analysis) []hit {
	var out []hit
	for _, m := range a.modes {
		if !looserModes[m.to] || m.from == "bypassPermissions" {
			continue
		}
		out = append(out, hit{at: m.at, evidence: m.from + " → " + m.to})
	}
	return out
}

// ─── Efficiency ──────────────────────────────────────────────────────────────

func retryIdentical(a *analysis) []hit {
	var out []hit
	failed := map[string]bool{}
	for _, s := range a.steps {
		if editTools[s.Tool] {
			clear(failed)
			continue
		}
		if s.Tool != "Bash" {
			continue
		}
		cmd := strings.TrimSpace(s.command())
		if failed[cmd] {
			out = append(out, hit{step: s, evidence: cmd})
		}
		failed[cmd] = s.failed
	}
	return out
}

func pollLoops(a *analysis) []hit {
	var out []hit
	for _, s := range a.steps {
		if s.bash != nil && !s.denied && len(s.bash.polls) > 0 {
			out = append(out, hit{step: s, evidence: s.bash.polls[0].src})
		}
	}
	return out
}

func longSleep(c simpleCmd, s *step, _ *analysis) bool {
	if s.denied || c.name != "sleep" || c.inLoop || len(c.args) < 2 {
		return false
	}
	return sleepDuration(c.args[1:]) >= 5*time.Second
}

// sleepDuration parses sleep's operands (summed, as GNU sleep does).
func sleepDuration(args []string) time.Duration {
	var total time.Duration
	for _, a := range args {
		mult := time.Second
		switch {
		case strings.HasSuffix(a, "s"):
			a = strings.TrimSuffix(a, "s")
		case strings.HasSuffix(a, "m"):
			a, mult = strings.TrimSuffix(a, "m"), time.Minute
		case strings.HasSuffix(a, "h"):
			a, mult = strings.TrimSuffix(a, "h"), time.Hour
		}
		f, err := strconv.ParseFloat(a, 64)
		if err != nil {
			continue
		}
		total += time.Duration(f * float64(mult))
	}
	return total
}

// errorRateMin* gate the error-rate finding.
const (
	errorRateMinCalls = 10
	errorRateMin      = 0.25
)

func errorRate(a *analysis) []hit {
	m := a.metrics
	if m.ToolCalls < errorRateMinCalls || m.ErrorRate < errorRateMin {
		return nil
	}
	return []hit{{
		evidence: fmt.Sprintf("%d of %d tool calls failed", m.ToolErrors, m.ToolCalls),
		why:      fmt.Sprintf("%.0f%% of tool calls errored", m.ErrorRate*100),
	}}
}

func repeatReads(a *analysis) []hit {
	var out []hit
	type readKey struct {
		path          string
		offset, limit any
	}
	seen := map[readKey]bool{}
	for _, s := range a.steps {
		switch {
		case s.Tool == "Read":
			if s.failed || s.denied {
				continue
			}
			p := s.filePath()
			k := readKey{p, s.Input["offset"], s.Input["limit"]}
			if seen[k] {
				tok := s.ReadRunes / 4
				a.metrics.RepeatReads++
				a.metrics.WastedTokensEst += tok
				why := "read the same file again with no change to it in between"
				if tok > 0 {
					why += fmt.Sprintf(" (~%d tokens)", tok)
				}
				out = append(out, hit{step: s, evidence: "Read " + s.relPath(p), why: why})
			}
			seen[k] = true
		case editTools[s.Tool]:
			p := s.filePath()
			for k := range seen {
				if k.path == p {
					delete(seen, k)
				}
			}
		case s.Tool == "Bash":
			// A command naming the file may have changed it.
			cmd := s.command()
			for k := range seen {
				if strings.Contains(cmd, filepath.Base(k.path)) {
					delete(seen, k)
				}
			}
		}
	}
	return out
}

// shellForBuiltin fires once per command name (cat, grep, ...), at its first
// use, with the number of uses: the habit is one finding, not one per call.
func shellForBuiltin(a *analysis) []hit {
	if a.pi {
		return nil
	}
	type use struct {
		h     hit
		why   string
		count int
	}
	var order []string
	uses := map[string]*use{}
	for _, s := range a.steps {
		if s.bash == nil || s.denied {
			continue
		}
		for _, c := range s.bash.cmds {
			why := builtinFor(c)
			if why == "" {
				continue
			}
			u := uses[c.name]
			if u == nil {
				u = &use{h: hit{step: s, evidence: c.src}, why: why}
				uses[c.name] = u
				order = append(order, c.name)
			}
			u.count++
			break
		}
	}
	out := make([]hit, 0, len(order))
	for _, name := range order {
		u := uses[name]
		u.h.why = u.why
		if u.count > 1 {
			u.h.why = fmt.Sprintf("%s (×%d)", u.why, u.count)
		}
		out = append(out, u.h)
	}
	return out
}

// builtinFor names the built-in tool c should have been, or "".
func builtinFor(c simpleCmd) string {
	if c.pipedIn || c.heredoc || len(c.outFiles) > 0 {
		return ""
	}
	_, ops := flagsAndOperands(c.args)
	switch c.name {
	case "cat", "head", "tail":
		if len(ops) > 0 {
			return c.name + " to read a file; the Read tool exists"
		}
	case "grep", "rg", "egrep":
		return c.name + " to search; the Grep tool exists"
	case "find":
		if !hasArg(c.args, "-exec", "-execdir", "-delete", "-ok") {
			return "find to list files; the Glob tool exists"
		}
	case "ls":
		return "ls to list files; the Glob tool exists"
	case "sed":
		flags, _ := flagsAndOperands(c.args)
		for _, f := range flags {
			if f == "-i" || strings.HasPrefix(f, "-i") || f == "--in-place" || strings.HasPrefix(f, "--in-place=") {
				return "sed -i to edit; the Edit tool exists"
			}
		}
	}
	return ""
}

func editFailures(a *analysis) []hit {
	var out []hit
	for _, s := range a.steps {
		if !editTools[s.Tool] || !s.failed {
			continue
		}
		why := "the edit failed and had to be redone"
		l := strings.ToLower(s.ResultHead)
		switch {
		case strings.Contains(l, "not been read"), strings.Contains(l, "read it first"):
			why = "edited a file it had not read first"
		case strings.Contains(l, "not found"):
			why = "old_string was not found in the file"
		case strings.Contains(l, "modified since"):
			why = "the file changed since it was read"
		case strings.Contains(l, "matches"):
			why = "old_string matched more than once"
		}
		out = append(out, hit{step: s, evidence: s.Tool + " " + s.relPath(s.filePath()) + ": " + firstLine(stripTags(s.ResultHead)), why: why})
	}
	return out
}

func idleGaps(a *analysis) []hit {
	if a.p.IdleGap <= 0 {
		return nil
	}
	var out []hit
	for _, s := range a.steps {
		if s.ModelGap >= a.p.IdleGap {
			out = append(out, hit{
				step:     s,
				evidence: s.Tool + " " + s.Label,
				why:      race.Friendly(s.ModelGap.Round(time.Second)) + " with no activity inside a turn",
			})
		}
	}
	return out
}

// stripTags drops Claude Code's <tool_use_error> wrapper.
func stripTags(s string) string {
	return strings.NewReplacer("<tool_use_error>", "", "</tool_use_error>", "").Replace(s)
}

// firstLine is the first non-empty line of s.
func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}
