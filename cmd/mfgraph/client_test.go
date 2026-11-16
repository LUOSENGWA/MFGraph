package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/api"
	"github.com/LUOSENGWA/MFGraph/internal/authz"
	"github.com/LUOSENGWA/MFGraph/internal/core"
	"github.com/LUOSENGWA/MFGraph/internal/ids"
	"github.com/LUOSENGWA/MFGraph/internal/ports/memory"
	"github.com/LUOSENGWA/MFGraph/internal/treestore"
)

const cliToken = "cli-test-token-0123456789"

// apiHandler serves the API on the in-memory ports (the server binary
// needs a database; the end-to-end tests run the CLI against it).
func apiHandler(t *testing.T, prefix string) http.Handler {
	t.Helper()
	store := memory.NewStore()
	objs := memory.NewBlobStore()
	trees, err := treestore.New(objs, func(ctx context.Context, ws string) (string, error) {
		w, err := store.GetWorkspace(ctx, nil, ws)
		return w.StoragePrefix, err
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := memory.NewClock(time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC))
	svc, err := core.New(core.Config{TenantID: "00000000000000000000000000"}, core.Deps{Store: store, Objects: objs, Trees: trees,
		Authz: authz.ProjectOwner{}, Clock: clock, IDs: memory.NewIDGen(clock)})
	if err != nil {
		t.Fatal(err)
	}
	authn, err := api.NewStaticTokens(map[string]string{cliToken: "cli-user"})
	if err != nil {
		t.Fatal(err)
	}
	return api.NewHandler(api.Options{Authenticator: authn, IDGen: ids.NewGenerator(), Service: svc, APIPrefix: prefix,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

// withEnv runs the CLI with the given environment.
func withEnv(t *testing.T, env map[string]string) func(args ...string) (int, string, string) {
	t.Helper()
	old := getenv
	t.Cleanup(func() { getenv = old })
	getenv = func(k string) string { return env[k] }
	return func(args ...string) (int, string, string) { return runCLI(args...) }
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// succeeded returns a function that checks a successful run and returns
// its trimmed output: ok(cli(...)).
func succeeded(t *testing.T) func(code int, out, errOut string) string {
	return func(code int, out, errOut string) string {
		t.Helper()
		if code != 0 {
			t.Fatalf("exit %d, stderr %s", code, errOut)
		}
		return strings.TrimSpace(out)
	}
}

// TestCLISmokeInProcess runs every client command against an in-process
// server, in the order a user would.
func TestCLISmokeInProcess(t *testing.T) {
	srv := httptest.NewServer(apiHandler(t, ""))
	defer srv.Close()
	cli := withEnv(t, map[string]string{envURL: srv.URL, envToken: cliToken})
	ok := succeeded(t)
	dir := t.TempDir()
	fa, fb, fc := writeFile(t, dir, "a", "hello\n"), writeFile(t, dir, "b", "second file\n"), writeFile(t, dir, "c", "changed\n")

	project := ok(cli("project", "create", "demo"))
	wsLine := ok(cli("workspace", "create", project, "--name", "drawings"))
	ws, mainField, _ := strings.Cut(wsLine, "\t")
	branch := strings.TrimPrefix(mainField, "main_branch=")
	if len(ws) != 26 || len(branch) != 26 {
		t.Fatalf("workspace create printed %q", wsLine)
	}
	if out := ok(cli("upload", ws, fa, fb)); !strings.Contains(out, "sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03\t6\tstaged\t"+fa) {
		t.Fatalf("upload printed %q", out)
	}
	s1 := ok(cli("commit", ws, "-m", "first", "a.txt="+fa, "docs/b.txt="+fb))
	s2 := ok(cli("commit", ws, "--branch", branch, "a.txt="+fc, "--delete", "docs/b.txt"))
	if out := ok(cli("log", ws)); !strings.HasPrefix(out, s2+"\t") || !strings.Contains(out, "\n"+s1+"\t") ||
		!strings.Contains(out, "manual\tcli-user\t2 files\tfirst") {
		t.Fatalf("log printed %q", out)
	}
	if out := ok(cli("log", ws, "--branch", branch)); strings.Count(out, "\n") != 1 {
		t.Fatalf("log of the branch printed %q", out)
	}
	if out := ok(cli("ls", s1)); !strings.Contains(out, "file\t644\t6\t") || !strings.Contains(out, "\tdocs/") {
		t.Fatalf("ls printed %q", out)
	}
	if out := ok(cli("ls", s1, "docs")); !strings.HasSuffix(out, "\tdocs/b.txt") {
		t.Fatalf("ls docs printed %q", out)
	}
	if code, out, errOut := cli("cat", s1, "a.txt"); code != 0 || out != "hello\n" {
		t.Fatalf("cat: %d %q %s", code, out, errOut)
	}
	if out := ok(cli("diff", s2)); out != "modified\ta.txt\ndeleted\tdocs/b.txt" {
		t.Fatalf("diff printed %q", out)
	}
	if out := ok(cli("diff", s2, "--base", s2)); out != "" {
		t.Fatalf("diff with itself printed %q", out)
	}
	s3 := ok(cli("restore", s1, "--branch", branch, "--path", "docs", "-m", "bring docs back"))
	if code, out, _ := cli("cat", s3, "docs/b.txt"); code != 0 || out != "second file\n" {
		t.Fatalf("cat after restore: %d %q", code, out)
	}
	s4 := ok(cli("restore", s1, "--branch", branch))
	if code, out, _ := cli("cat", s4, "a.txt"); code != 0 || out != "hello\n" {
		t.Fatalf("cat after full restore: %d %q", code, out)
	}
	// --json prints the response.
	var p map[string]any
	if code, out, _ := cli("project", "create", "x", "--json"); code != 0 || json.Unmarshal([]byte(out), &p) != nil || p["name"] != "x" {
		t.Fatalf("--json: %d %q", code, out)
	}

	// Errors of the server are printed as received; the exit status is 1.
	code, _, errOut := cli("cat", s1, "missing.txt")
	if code != 1 || !strings.Contains(errOut, "404 Not Found") || !strings.Contains(errOut, `"code":"not_found"`) {
		t.Fatalf("missing file: %d %q", code, errOut)
	}
	code, _, errOut = cli("commit", ws, "../evil="+fa)
	if code != 1 || !strings.Contains(errOut, `"code":"invalid_path"`) || !strings.Contains(errOut, `"kind":"dot_segment"`) {
		t.Fatalf("invalid path: %d %q", code, errOut)
	}
	code, _, errOut = withEnv(t, map[string]string{envURL: srv.URL, envToken: "wrong-token-0123456789"})("log", ws)
	if code != 1 || !strings.Contains(errOut, `"code":"unauthenticated"`) {
		t.Fatalf("wrong token: %d %q", code, errOut)
	}
	// Mistakes in the command line: exit status 2.
	for _, args := range [][]string{{"commit", ws}, {"commit", ws, "nofile"}, {"restore", s1}, {"project"}, {"ls"},
		{"log", ws, "--bogus"}, {"upload", ws}} {
		if code, _, _ := cli(args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	if code, _, errOut := cli("upload", ws, filepath.Join(dir, "missing")); code != 1 || !strings.Contains(errOut, "no such file") {
		t.Errorf("upload of a missing file: %d %q", code, errOut)
	}
}

// TestCLIPrefixAndUnixSocket: --prefix moves the API, unix:<path> connects
// to a Unix domain socket.
func TestCLIPrefixAndUnixSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "mfc")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "api.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: apiHandler(t, "/internal/v1/files")}
	go srv.Serve(ln)
	defer srv.Close()
	cli := withEnv(t, map[string]string{envURL: "unix:" + sock, envToken: cliToken})
	ok := succeeded(t)
	if code, _, errOut := cli("project", "create", "p"); code != 1 || !strings.Contains(errOut, `"code":"not_found"`) {
		t.Fatalf("default prefix against a moved API: %d %q", code, errOut)
	}
	if id := ok(cli("project", "create", "p", "--prefix", "/internal/v1/files")); len(id) != 26 {
		t.Fatalf("project create through the socket printed %q", id)
	}
	cli = withEnv(t, map[string]string{envURL: "unix:" + sock, envToken: cliToken, envPrefix: "/internal/v1/files"})
	if id := ok(cli("project", "create", "q")); len(id) != 26 {
		t.Fatalf("MFGRAPH_API_PREFIX: %q", id)
	}
	if code, _, _ := withEnv(t, map[string]string{envURL: "ftp://x"})("log", "x"); code != 2 {
		t.Fatalf("bad URL scheme: exit %d", code)
	}
}
