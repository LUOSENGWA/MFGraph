//go:build e2e

package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runCLI runs the mfgraph client against the server as principal A.
func (s *server) runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(cliBin, args...)
	cmd.Env = append(os.Environ(), "MFGRAPH_URL="+s.base, "MFGRAPH_TOKEN="+tokenA)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return code, out.String(), errOut.String()
}

// TestCLISmoke runs every client command of the mfgraph CLI against the
// real server: project create, workspace create, upload, commit (with
// --delete), log, ls, cat, diff and restore, and checks that a server error
// is printed as received with exit status 1.
func TestCLISmoke(t *testing.T) {
	s := start(t, newEnv(t))
	ok := func(args ...string) string {
		t.Helper()
		code, out, errOut := s.runCLI(t, args...)
		if code != 0 {
			t.Fatalf("mfgraph %s: exit %d: %s", strings.Join(args, " "), code, errOut)
		}
		return strings.TrimSpace(out)
	}
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	fa, fb, fc := write("a", contentA), write("b", contentB), write("c", "changed\n")

	project := ok("project", "create", "cli project")
	ws, main, _ := strings.Cut(ok("workspace", "create", project), "\t")
	branch := strings.TrimPrefix(main, "main_branch=")
	if out := ok("upload", ws, fa); !strings.HasPrefix(out, sha(contentA)+"\t6\t") {
		t.Fatalf("upload: %q", out)
	}
	s1 := ok("commit", ws, "-m", "first", "a.txt="+fa, "docs/b.txt="+fb)
	s2 := ok("commit", ws, "--branch", branch, "a.txt="+fc, "--delete", "docs/b.txt")
	if out := ok("log", ws); !strings.HasPrefix(out, s2) || !strings.Contains(out, s1) {
		t.Fatalf("log: %q", out)
	}
	if out := ok("ls", s1); !strings.Contains(out, "a.txt") || !strings.Contains(out, "docs/") {
		t.Fatalf("ls: %q", out)
	}
	if code, out, _ := s.runCLI(t, "cat", s1, "docs/b.txt"); code != 0 || out != contentB {
		t.Fatalf("cat: %d %q", code, out)
	}
	if out := ok("diff", s2); out != "modified\ta.txt\ndeleted\tdocs/b.txt" {
		t.Fatalf("diff: %q", out)
	}
	s3 := ok("restore", s1, "--branch", branch)
	if code, out, _ := s.runCLI(t, "cat", s3, "a.txt"); code != 0 || out != contentA {
		t.Fatalf("cat after restore: %d %q", code, out)
	}
	code, _, errOut := s.runCLI(t, "cat", s3, "missing.txt")
	if code != 1 || !strings.Contains(errOut, `"code":"not_found"`) {
		t.Fatalf("missing file: %d %q", code, errOut)
	}
}
