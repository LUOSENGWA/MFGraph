package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestVersionAndHelp(t *testing.T) {
	if code, out, _ := runCLI("version"); code != 0 || !strings.HasPrefix(out, "mfgraph ") {
		t.Fatalf("version: %d %q", code, out)
	}
	if code, out, _ := runCLI("help"); code != 0 || !strings.Contains(out, "debug") {
		t.Fatalf("help: %d %q", code, out)
	}
	if code, _, errOut := runCLI("commit"); code != 1 || !strings.Contains(errOut, "not implemented") {
		t.Fatalf("placeholder: %d %q", code, errOut)
	}
	if code, _, _ := runCLI("bogus"); code != 2 {
		t.Fatalf("unknown command exit %d", code)
	}
	if code, _, _ := runCLI(); code != 2 {
		t.Fatalf("no args exit %d", code)
	}
}

func TestDebugHash(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(p, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runCLI("debug", "hash", p)
	want := "sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03  6  " + p + "\n"
	if code != 0 || out != want {
		t.Fatalf("debug hash: %d %q", code, out)
	}
	if code, _, _ := runCLI("debug", "hash", filepath.Join(dir, "missing")); code != 1 {
		t.Fatalf("missing file exit %d", code)
	}
}

// TestDebugTreeMatchesVectors materializes the small tree vectors, runs the
// debug command on them and compares the root and the written node files.
func TestDebugTreeMatchesVectors(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "tree.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cases []struct {
			Name  string `json:"name"`
			Files []struct {
				Path    string `json:"path"`
				Content string `json:"content"`
				Mode    int    `json:"mode"`
			} `json:"files"`
			Generate  []json.RawMessage `json:"generate"`
			Root      string            `json:"root"`
			NodeCount int               `json:"node_count"`
			Error     string            `json:"error"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range doc.Cases {
		if c.Error != "" || len(c.Generate) > 0 {
			continue
		}
		n++
		t.Run(c.Name, func(t *testing.T) {
			src, out := filepath.Join(t.TempDir(), "in"), filepath.Join(t.TempDir(), "nodes")
			if err := os.MkdirAll(src, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, f := range c.Files {
				target := filepath.Join(src, filepath.FromSlash(f.Path))
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					t.Fatal(err)
				}
				perm := os.FileMode(0o644)
				if f.Mode == 493 {
					perm = 0o755
				}
				if err := os.WriteFile(target, []byte(f.Content), perm); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(target, perm); err != nil { // umask-independent
					t.Fatal(err)
				}
			}
			code, stdout, stderr := runCLI("debug", "tree", src, "--out", out)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			lines := strings.Split(strings.TrimSpace(stdout), "\n")
			if last := lines[len(lines)-1]; last != "root "+c.Root {
				t.Fatalf("last line %q, want root %s", last, c.Root)
			}
			entries, err := os.ReadDir(out)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != c.NodeCount {
				t.Fatalf("%d node files, want %d", len(entries), c.NodeCount)
			}
			for _, e := range entries {
				data, err := os.ReadFile(filepath.Join(out, e.Name()))
				if err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(data)
				if hex.EncodeToString(sum[:])+".json" != e.Name() || bytes.HasSuffix(data, []byte("\n")) {
					t.Fatalf("node file %s does not hash to its name (or has a trailing newline)", e.Name())
				}
			}
		})
	}
	if n == 0 {
		t.Fatal("no small cases found")
	}
}

func TestDebugTreeWarningsAndErrors(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("a.txt", "a")
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	code, out, errOut := runCLI("debug", "tree", "--out", filepath.Join(t.TempDir(), "o"), dir)
	if code != 0 || !strings.Contains(errOut, `skipping symlink "link"`) || !strings.Contains(errOut, `directory "empty" has no files`) {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out, errOut)
	}
	if !strings.Contains(out, "files 1  distinct nodes 1") {
		t.Fatalf("summary missing: %s", out)
	}

	mustWrite(`bad\name.txt`, "x") // a legal file name on Unix, an illegal path here
	code, _, errOut = runCLI("debug", "tree", dir)
	if code != 1 || !strings.Contains(errOut, "backslash") {
		t.Fatalf("invalid path: exit %d, stderr %q", code, errOut)
	}

	if code, _, _ := runCLI("debug", "tree"); code != 2 {
		t.Fatalf("missing argument exit %d", code)
	}
	if code, _, _ := runCLI("debug", "tree", filepath.Join(dir, "a.txt")); code != 1 {
		t.Fatalf("file instead of directory exit %d", code)
	}
	if code, _, _ := runCLI("debug", "nope"); code != 2 {
		t.Fatalf("unknown debug command exit %d", code)
	}
}

// TestDebugWarnsOnMismatchedUnicodeTables: the debug tools only warn and go
// on when the Unicode tables of the build do not match V.
func TestDebugWarnsOnMismatchedUnicodeTables(t *testing.T) {
	f := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(f, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"debug", "hash", f}, &out, &errOut); code != 0 || errOut.Len() != 0 {
		t.Fatalf("matching tables: exit %d, stderr %q", code, errOut.String())
	}
	defer func(g func() (string, string, string)) { unicodeTables = g }(unicodeTables)
	unicodeTables = func() (string, string, string) { return "17.0.0", "17.0.0", "17.0.0" }
	out.Reset()
	if code := run([]string{"debug", "hash", f}, &out, &errOut); code != 0 {
		t.Fatalf("debug hash with mismatched tables: exit %d", code)
	}
	if !strings.Contains(errOut.String(), "warning") || !strings.Contains(errOut.String(), "15.0.0") ||
		!strings.Contains(errOut.String(), "17.0.0") {
		t.Errorf("no warning naming the versions: %q", errOut.String())
	}
	if !strings.Contains(out.String(), "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824") {
		t.Errorf("debug hash did not run: %q", out.String())
	}
}
