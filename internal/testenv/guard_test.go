package testenv

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryPackageIsolated fails when a package with tests has no TestMain
// calling testenv.Main or testenv.Setup, so a new package cannot quietly run
// its tests against live state.
func TestEveryPackageIsolated(t *testing.T) {
	root := moduleRoot(t)
	dirs := testDirs(t, root)
	if len(dirs) < 10 {
		t.Fatalf("found only %d test packages under %s; is the walk broken?", len(dirs), root)
	}
	for _, dir := range dirs {
		rel, _ := filepath.Rel(root, dir)
		ok, err := callsHelper(dir)
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		if !ok {
			t.Errorf("%s: no TestMain calling testenv.Main(m) or testenv.Setup(); add one (see internal/testenv)", rel)
		}
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test dir")
		}
		dir = parent
	}
}

// testDirs returns every directory under root holding _test.go files,
// skipping nested worktrees, node_modules, testdata and hidden dirs.
func testDirs(t *testing.T, root string) []string {
	t.Helper()
	seen := map[string]bool{}
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
				name == "node_modules" || name == "testdata" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), "_test.go") {
			if dir := filepath.Dir(path); !seen[dir] {
				seen[dir] = true
				dirs = append(dirs, dir)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return dirs
}

// callsHelper reports whether a TestMain among dir's test files calls
// testenv.Main or testenv.Setup (unqualified inside this package).
func callsHelper(dir string) (bool, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		return false, err
	}
	fset := token.NewFileSet()
	for _, path := range files {
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return false, err
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != "TestMain" || fn.Body == nil {
				continue
			}
			if bodyCallsHelper(fn.Body, f.Name.Name == "testenv") {
				return true, nil
			}
		}
	}
	return false, nil
}

func bodyCallsHelper(body *ast.BlockStmt, local bool) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return !found
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			if x, ok := fun.X.(*ast.Ident); ok && x.Name == "testenv" && isHelper(fun.Sel.Name) {
				found = true
			}
		case *ast.Ident:
			if local && isHelper(fun.Name) {
				found = true
			}
		}
		return !found
	})
	return found
}

func isHelper(name string) bool { return name == "Main" || name == "Setup" }
