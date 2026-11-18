package main

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/alecthomas/kong"

	"github.com/blsmth/agentctl/internal/dispatch"
)

var (
	preambleCmdRe  = regexp.MustCompile("`(agentctl [^`]*)`")
	preambleWordRe = regexp.MustCompile(`^[a-z][a-z-]*$`)
	preambleFlagRe = regexp.MustCompile(`--[a-z][a-z-]*`)
	contextCaseRe  = regexp.MustCompile(`case "([a-z-]+)"`)
)

// nodeHasFlag reports whether flag name is declared on n or any ancestor.
func nodeHasFlag(n *kong.Node, name string) bool {
	for ; n != nil; n = n.Parent {
		for _, f := range n.Flags {
			if f.Name == name {
				return true
			}
		}
	}
	return false
}

func findChild(n *kong.Node, word string) *kong.Node {
	for _, c := range n.Children {
		if c.Type != kong.CommandNode {
			continue
		}
		if c.Name == word {
			return c
		}
		for _, a := range c.Aliases {
			if a == word {
				return c
			}
		}
	}
	return nil
}

// checkInvocation validates one backticked `agentctl ...` span against the
// Kong model and returns a problem description, or "".
func checkInvocation(model *kong.Node, ctxCases map[string]bool, span string) string {
	fields := strings.Fields(span)[1:] // drop "agentctl"
	if len(fields) == 0 || strings.HasSuffix(fields[0], ":") {
		return "" // "agentctl notice:" is a line prefix, not a command
	}
	node := model
	var path []string
	for i, w := range fields {
		if !preambleWordRe.MatchString(w) {
			break
		}
		if node == model || node.Name != "context" {
			if child := findChild(node, w); child != nil {
				node = child
				path = append(path, w)
				continue
			}
			if node == model || len(node.Children) > 0 && node.DefaultCmd == nil && !hasArgs(node) {
				return "unknown command " + strings.Join(append(path, w), " ")
			}
			break
		}
		// context takes raw args: subcommands come from internal/cmd/context.go.
		if i > 0 && !ctxCases[w] {
			return "context does not accept subcommand " + w
		}
		break
	}
	if node.Name == "context" && node != model {
		for _, f := range preambleFlagRe.FindAllString(span, -1) {
			if !ctxCases[f] {
				return "context does not accept " + f
			}
		}
		return ""
	}
	for _, f := range preambleFlagRe.FindAllString(span, -1) {
		if !nodeHasFlag(node, strings.TrimPrefix(f, "--")) {
			return "unknown flag " + f + " for `agentctl " + strings.Join(path, " ") + "`"
		}
	}
	return ""
}

func hasArgs(n *kong.Node) bool { return len(n.Positional) > 0 }

func TestPreamblesMatchCLI(t *testing.T) {
	var cli CLI
	k, err := kong.New(&cli, kong.Name("agentctl"))
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}
	src, err := os.ReadFile("internal/cmd/context.go")
	if err != nil {
		t.Fatal(err)
	}
	ctxCases := map[string]bool{}
	for _, m := range contextCaseRe.FindAllStringSubmatch(string(src), -1) {
		ctxCases[m[1]] = true
		ctxCases["--"+m[1]] = true // flags appear as case "--agent"; tolerate both forms
	}
	for _, sub := range []string{"set", "get", "list", "clear", "push"} {
		if !ctxCases[sub] {
			t.Fatalf("context.go no longer handles %q; update the preamble guard", sub)
		}
	}

	for name, text := range dispatch.AllPreambles() {
		spans := preambleCmdRe.FindAllStringSubmatch(text, -1)
		if len(spans) == 0 && !strings.Contains(name, "Section") {
			t.Errorf("%s: no agentctl invocations found; the extractor is broken", name)
		}
		for _, m := range spans {
			if msg := checkInvocation(k.Model.Node, ctxCases, m[1]); msg != "" {
				t.Errorf("%s: `%s`: %s", name, m[1], msg)
			}
		}
	}
}

func TestCheckInvocation(t *testing.T) {
	var cli CLI
	k, err := kong.New(&cli, kong.Name("agentctl"))
	if err != nil {
		t.Fatal(err)
	}
	ctxCases := map[string]bool{"set": true, "get": true, "list": true, "clear": true, "push": true, "--agent": true, "--tag": true}
	tests := []struct {
		span string
		bad  bool
	}{
		{"agentctl dispatch --issue <N>", false},
		{"agentctl status peek @N [--lines N]", false},
		{"agentctl status list [--json] [--blocked]", false},
		{"agentctl tickets", false},
		{"agentctl context set <key> <value> --agent <name> --tag <t>", false},
		{"agentctl notice:", false},
		{"agentctl send @N hi", true},
		{"agentctl dispatch --nonsense", true},
		{"agentctl status peek @N --nope", true},
		{"agentctl context frobnicate x", true},
		{"agentctl context set k v --bogus", true},
	}
	for _, tt := range tests {
		t.Run(tt.span, func(t *testing.T) {
			got := checkInvocation(k.Model.Node, ctxCases, tt.span)
			if (got != "") != tt.bad {
				t.Fatalf("checkInvocation(%q) = %q, bad=%v", tt.span, got, tt.bad)
			}
		})
	}
}
