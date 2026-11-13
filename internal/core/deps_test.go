package core_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/LUOSENGWA/MFGraph/"

// imports returns the imports of the non-test Go files of a package dir.
func imports(t *testing.T, dir string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			out[p] = true
		}
	}
	return out
}

func isStdlib(p string) bool { return !strings.Contains(strings.SplitN(p, "/", 2)[0], ".") }

// TestDependencyRule enforces the layering:
//
//	core       -> stdlib, ports, contenthash, pathx, tree, storagekey
//	ports      -> stdlib, contenthash
//	pure pkgs  -> stdlib, each other (and golang.org/x/text for pathx)
//
// Adapters (api, migrate, config, ports/memory, ...) must never be imported
// by these packages.
func TestDependencyRule(t *testing.T) {
	pure := []string{"internal/contenthash", "internal/pathx", "internal/tree", "internal/storagekey"}
	rules := map[string][]string{
		"internal/core":  append([]string{"internal/ports"}, pure...),
		"internal/ports": {"internal/contenthash"},
	}
	for _, p := range pure {
		rules[p] = append([]string{"golang.org/x/text/"}, pure...)
	}
	for pkg, allowed := range rules {
		for imp := range imports(t, filepath.Join("..", "..", pkg)) {
			if isStdlib(imp) {
				continue
			}
			ok := false
			for _, a := range allowed {
				if strings.HasSuffix(a, "/") {
					ok = ok || strings.HasPrefix(imp, a)
				} else {
					ok = ok || imp == module+a
				}
			}
			if !ok {
				t.Errorf("%s must not import %s", pkg, imp)
			}
		}
	}
}
