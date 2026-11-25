package hooks

import (
	"strings"
	"testing"
)

// serveToggleTestAppend is the denied command from the audit log (session
// bd79a984): a heredoc appending a Go test to a file that sed and python -c
// then edit in place. The body mentions `systemctl --user stop
// agentctl-serve` as a Go string.
const serveToggleTestAppend = `cat >> internal/cmd/serve_toggle_test.go <<'EOF'

// ` + "`serve stop`" + ` only switches the phone off; ` + "`serve stop --all`" + ` is the old
// full stop.
func TestServeStopAction(t *testing.T) {
	dir := t.TempDir()
	alive := true
	c := servectl.Controller{
		Dir:     dir,
		GOOS:    "linux",
		Running: func(string) (int, bool) { return 1, alive },
	}
	msg, err := serveStopAction(false)(c)
	if err != nil || !servectl.PhoneOff(dir) || !strings.HasPrefix(msg, "phone access off") {
		t.Fatalf("stop = %q, %v (phone off %v)", msg, err, servectl.PhoneOff(dir))
	}
	msg, err = serveStopAction(true)(c)
	if err != nil || msg != "Run: systemctl --user stop agentctl-serve" {
		t.Fatalf("stop --all = %q, %v", msg, err)
	}
}
EOF
sed -i '' 's|	"errors"\n	"testing"|X|' internal/cmd/serve_toggle_test.go
python3 -c "
p='internal/cmd/serve_toggle_test.go'
s=open(p).read()
s=s.replace('\t\"errors\"\n\t\"testing\"','\t\"errors\"\n\t\"strings\"\n\t\"testing\"',1)
open(p,'w').write(s)"
go test ./internal/cmd/ -run 'TestServe|TestRunServe|TestPrintServe|Artifact' 2>&1 | tail -10`

// goSourceHeredoc writes Go source that names serve's socket and pidfile.
const goSourceHeredoc = `cat > internal/remoteapproval/names.go <<'EOF'
package remoteapproval

const (
	ControlSocket = "control.sock"
	PIDFile       = "serve.pid"
)
EOF
gofmt -l internal/remoteapproval/names.go && go build ./internal/remoteapproval/`

// TestTamperRules_DataIsNotTarget covers the false positives where serve's
// paths or a history store name appear only as data: a search pattern or a
// heredoc body written to a repo file. Each command was denied before.
func TestTamperRules_DataIsNotTarget(t *testing.T) {
	pinAllTamperDirs(t)
	state := pinHistoryDir(t)
	cwd := t.TempDir()
	for _, tc := range []struct{ name, command string }{
		// d7f884b0: the pattern names the serve state dir; the file is a doc.
		{"grep pattern names serve dir", "grep -n \"State lives in \\`\\$XDG_STATE_HOME/agentctl/serve/\\` (0700): the local CA and server certificate,\" docs/configuration.md"},
		// 69eb72c7: the pattern joined to the cd read as <repo>/agentctl/agent-history.
		{"grep agent-history after cd", `cd /Users/brendansmith/code/personal/agentctl; grep -rl "agent-history" .agentctl/logs ~/code/*/*/.agentctl/logs 2>/dev/null | head -5; grep -rn "SESSION_ID" internal/hooks/janitor.go internal/detach/*.go | head -3; git -C . diff --stat >/dev/null; sed -n 1,40p internal/hooks/janitor.go | head -40`},
		// bd79a984: the body was kept because sed and python -c name the file.
		{"heredoc then in-place edits", serveToggleTestAppend},
		{"heredoc Go source names socket", goSourceHeredoc},
		// 4f6cf261: the bracket in the grep pattern counted as a glob next to an ls of the state dir.
		{"grep bracket pattern and ls state root", `ps -Ao pid,lstart,command | grep -E '[t]mux' | head; echo ---; ls -lt ` + state + `/ | head -30`},
		{"grep -e pattern", `grep -rn -e "serve.pid" -e 'control\.sock' internal/`},
		{"rg pattern", `rg -n 'state/agentctl/serve' docs internal`},
		{"grep -- pattern", `grep -n -- "~/.local/state/agentctl/serve" docs/*.md`},
		{"grep long regexp", `grep --regexp "control.sock" -r internal`},
		{"heredoc then cat and sed -n", "cat > notes.md <<'EOF'\nagentctl serve stop turns the phone off; serve.pid stays\nEOF\nsed -n '1,3p' notes.md; wc -l notes.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if r := checkGlobalDenyListIn(tc.command, cwd); r != "" {
				t.Errorf("denied: %s\n%s", r, tc.command)
			}
		})
	}
}

// TestTamperRules_TargetsStillDenied: path operands, write targets, git
// against a store, process kills, executed heredocs and the state dir root
// stay denied next to the masking above.
func TestTamperRules_TargetsStillDenied(t *testing.T) {
	pinAllTamperDirs(t)
	state := pinHistoryDir(t)
	store := state + "/agent-history.git"
	cwd := t.TempDir()
	sd := "~/.local/state/agentctl/serve"
	key := "/Users/u/.local/state/agentctl/serve/ca.key"
	blow := strings.Repeat("D=x$D$D; ", 40)
	for _, tc := range []struct{ name, command string }{
		{"redirect into serve dir", `echo x > ` + sd + `/devices.json`},
		{"rm serve dir", `rm -rf ` + sd},
		{"mv onto socket", `mv /tmp/x ` + sd + `/control.sock`},
		{"cp into serve dir", `cp /tmp/x ` + sd + `/ca.key`},
		{"chmod serve dir", `chmod 777 ` + sd},
		{"ln pidfile", `ln -sf /tmp/x ` + sd + `/serve.pid`},
		{"kill by pidfile", `kill $(cat ` + sd + `/serve.pid)`},
		{"pkill serve", `pkill -f "agentctl serve"`},
		{"grep path operand", `grep "push:" ` + sd + `/serve.log`},
		{"grep unquoted path operand", `grep -n x ` + sd + `/devices.json`},
		{"grep pattern file", `grep -f ` + key + ` /dev/null`},
		{"grep operand before -e", `grep "` + key + `" -e x`},
		{"grep abbreviated --file", `grep --fi "` + key + `" x`},
		{"grep expansion before pattern", `grep $OPTS "` + key + `"`},
		{"grep pattern in substitution", `grep "x" $(echo ` + key + `)`},
		{"rg path operand", `rg "x" ` + sd},
		{"cat serve log", `cat ` + sd + `/serve.log`},
		{"ls serve dir", `ls ` + sd},
		{"cd serve dir then grep", `cd ` + sd + ` && grep -r "x" .`},
		{"heredoc piped to bash", "cat <<'EOF' | bash\nagentctl serve stop\nEOF"},
		{"heredoc to bash", "bash <<'EOF'\nagentctl serve stop\nEOF"},
		{"heredoc to python", "python3 - <<'EOF'\nimport os\nos.remove('" + key + "')\nEOF"},
		{"heredoc to node", "node <<'EOF'\nrequire('fs').unlinkSync('" + sd + "/serve.pid')\nEOF"},
		{"heredoc script then python", "cat > /tmp/sc_edit.py <<'EOF'\nopen('" + sd + "/serve.pid','w')\nEOF\npython3 /tmp/sc_edit.py"},
		{"heredoc script, sed, then bash", "cat > s.sh <<'EOF'\nagentctl serve stop\nEOF\nsed -i '' 's/a/b/' s.sh; bash s.sh"},
		{"heredoc script cat piped to bash", "cat > s.sh <<'EOF'\nagentctl serve stop\nEOF\ncat s.sh | bash"},
		{"heredoc script copied by sed w", "cat > s.sh <<'EOF'\nagentctl serve stop\nEOF\nsed -n 'w t.sh' s.sh; bash t.sh"},
		{"heredoc script run by sed e", "cat > s.sh <<'EOF'\nagentctl serve stop\nEOF\nsed -n 'e' s.sh"},
		{"heredoc script exec by python -c", "cat > s.py <<'EOF'\nimport os; os.system('agentctl serve stop')\nEOF\npython3 -c \"exec(open('s.py').read())\""},
		{"heredoc script copied by cat", "cat > s.sh <<'EOF'\nagentctl serve stop\nEOF\ncat s.sh > t.sh; bash t.sh"},
		{"overflow with pattern", blow + `grep "x" ` + sd + `/devices.json`},
		{"git-dir store", `git --git-dir=` + store + ` log`},
		{"git -C store", `git -C ` + store + ` log --oneline -3`},
		{"GIT_DIR store", `GIT_DIR=` + store + ` git log`},
		{"rm store", `rm -rf ` + store},
		{"grep store path operand", `grep "x" ` + store + `/config`},
		{"for loop on state root", `for d in ` + state + `/*; do ls $d; done`},
		{"find on state root", `find ` + state + ` -name HEAD`},
		{"glob into store", `ls ` + state + `/agent-h*`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if checkGlobalDenyListIn(tc.command, cwd) == "" {
				t.Errorf("allowed:\n%s", tc.command)
			}
		})
	}
}

func TestSearchPatternArgs(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    []string // the raw words masked
	}{
		{`grep -n "a b" f`, []string{`"a b"`}},
		{`grep -rn -e 'x' -e "y" dir`, []string{`'x'`, `"y"`}},
		{`grep -A 3 'p' f`, []string{`'p'`}},
		{`grep -rne 'p' f`, []string{`'p'`}},
		{`grep --include=*.go -r 'p' internal`, []string{`'p'`}},
		{`rg -g '*.go' 'p' internal`, []string{`'p'`}},
		{`grep -- '-x' f`, []string{`'-x'`}},
		{`grep p f`, nil},            // unquoted: left visible
		{`grep "a$X" f`, nil},        // expands
		{`grep -f 'pats' f`, nil},    // -f: the operands are files
		{`grep 'f' -e x`, nil},       // a later -e makes 'f' a file
		{`grep --fil 'f' x`, nil},    // abbreviation
		{`grep $O 'p' f`, nil},       // may become -e or -f
		{`grep 'p' "$(cat x)"`, nil}, // substitution
		{`/usr/bin/grep 'p' f`, []string{`'p'`}},
		{`cat 'p' f`, nil},
	} {
		t.Run(tc.command, func(t *testing.T) {
			sc := scanShell(tc.command)
			var got []string
			for _, it := range sc.items {
				if it.cmd == nil {
					continue
				}
				for _, i := range it.cmd.searchPatternArgs() {
					got = append(got, it.cmd.words[i].raw)
				}
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInertSedScript(t *testing.T) {
	for _, tc := range []struct {
		script string
		want   bool
	}{
		{`s/a/b/`, true},
		{`s|a\|b|c|g`, true},
		{`1,40p`, true},
		{`$d`, true},
		{`/re/,/end/!d; s/x/y/2`, true},
		{"s/a/b/\ns/c/d/", true},
		{`s/a/b/e`, false},
		{`s/a/b/w out`, false},
		{`w out`, false},
		{`e`, false},
		{`1e date`, false},
		{`r other`, false},
		{`{p}`, false},
		{`s/a/b`, false},
		{`1,p`, false},
	} {
		if got := inertSedScript(tc.script); got != tc.want {
			t.Errorf("inertSedScript(%q) = %v, want %v", tc.script, got, tc.want)
		}
	}
}

func TestInertSedArgs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"-i", "", "s/a/b/", "f"}, true},
		{[]string{"-i.bak", "s/a/b/", "f"}, true},
		{[]string{"-n", "1,40p", "f"}, true},
		{[]string{"-e", "s/a/b/", "-e", "p", "f"}, true},
		{[]string{"-i", "s/a/b/", "w t.sh", "f"}, false}, // BSD: the script is "w t.sh"
		{[]string{"-f", "script.sed", "f"}, false},
		{[]string{"-n", "e", "f"}, false},
		{[]string{"f"}, false},
		{nil, false},
	} {
		if got := inertSedArgs(tc.args); got != tc.want {
			t.Errorf("inertSedArgs(%q) = %v, want %v", tc.args, got, tc.want)
		}
	}
}
