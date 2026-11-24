package sessionaudit

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestStaysPure: the analyzer reads no tmux, spawns nothing, makes no
// network calls and does not depend on the hook handlers.
func TestStaysPure(t *testing.T) {
	banned := []string{"internal/hooks", "internal/tmux", "os/exec", "net/http", "net"}
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		name := f.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			for _, b := range banned {
				if p == b || strings.HasSuffix(p, "/"+b) {
					t.Errorf("%s imports %s", name, p)
				}
			}
		}
	}
}
