//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

const (
	tokenA = "e2e-token-principal-a-0123456789"
	tokenB = "e2e-token-principal-b-0123456789"
	// The actor references of the two principals: one is a plain name (as a
	// host platform passes it), the other the ULID of a standalone principal.
	actorA = "e2e-alice"
	actorB = "01JD3X5Q8Z9W2V4T6R8P0N2MB0"
)

var serverBin, cliBin string

// skipReason is why the tests are skipped: the service needs a database.
const skipReason = "MFGRAPH_E2E_DB_DSN is not set: the acceptance tests need a database (MySQL 8.0 or seekdb); " +
	"see test/e2e/README.md"

// requireDB reports MFGRAPH_E2E_REQUIRE_DB=1: CI sets it, so that a job
// whose database settings got lost fails instead of skipping every test.
func requireDB() bool { return os.Getenv("MFGRAPH_E2E_REQUIRE_DB") == "1" }

func TestMain(m *testing.M) {
	if os.Getenv("MFGRAPH_E2E_DB_DSN") == "" {
		if requireDB() {
			fmt.Fprintln(os.Stderr, "MFGRAPH_E2E_REQUIRE_DB=1, but "+skipReason)
			os.Exit(1)
		}
		os.Exit(m.Run()) // every test skips; nothing to build
	}
	dir, err := os.MkdirTemp("", "mfgraph-e2e-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	serverBin, cliBin = os.Getenv("MFGRAPH_E2E_SERVER_BIN"), os.Getenv("MFGRAPH_E2E_CLI_BIN")
	for _, b := range []struct {
		bin *string
		pkg string
	}{{&serverBin, "mfgraph-server"}, {&cliBin, "mfgraph"}} {
		if *b.bin != "" {
			continue
		}
		*b.bin = filepath.Join(dir, b.pkg)
		build := exec.Command("go", "build", "-o", *b.bin, "github.com/LUOSENGWA/MFGraph/cmd/"+b.pkg)
		build.Stdout, build.Stderr = os.Stdout, os.Stderr
		if err := build.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "build "+b.pkg+":", err)
			os.Exit(1)
		}
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// env is the configuration of one server instance; restarting with the same
// env must find the same data.
type env map[string]string

// newEnv prepares storage and a fresh database on the server of
// MFGRAPH_E2E_DB_DSN; without it the test is skipped. With
// MFGRAPH_E2E_DB_WAIT (a duration) it waits that long for the database to
// answer (seekdb initializes a fresh data directory first).
func newEnv(t *testing.T) env {
	t.Helper()
	dsn := os.Getenv("MFGRAPH_E2E_DB_DSN")
	if dsn == "" {
		if requireDB() {
			t.Fatal("MFGRAPH_E2E_REQUIRE_DB=1, but " + skipReason)
		}
		t.Skip(skipReason)
	}
	e := env{
		"MFGRAPH_AUTH_TOKENS":       tokenA + "=" + actorA + "," + tokenB + "=" + actorB,
		"MFGRAPH_STORAGE_ROOT":      t.TempDir(),
		"MFGRAPH_LOG_FORMAT":        "text",
		"MFGRAPH_LOG_PSEUDONYM_KEY": "e2e-pseudonym-key-0123456789",
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	var wait time.Duration
	if v := os.Getenv("MFGRAPH_E2E_DB_WAIT"); v != "" {
		if wait, err = time.ParseDuration(v); err != nil {
			t.Fatal(err)
		}
	}
	for deadline := time.Now().Add(wait); ; time.Sleep(2 * time.Second) {
		if err = admin.Ping(); err == nil || time.Now().After(deadline) {
			break
		}
	}
	if err != nil {
		t.Fatalf("database of MFGRAPH_E2E_DB_DSN: %v", err)
	}
	name := fmt.Sprintf("mfgraph_e2e_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE DATABASE " + name + " CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec("DROP DATABASE " + name); admin.Close() })
	cfg.DBName = name
	e["MFGRAPH_DB_DSN"] = cfg.FormatDSN()
	e["MFGRAPH_MIGRATE_ON_START"] = "true"
	return e
}

type server struct {
	t       *testing.T
	cmd     *exec.Cmd
	base    string
	logs    *syncBuffer
	done    chan error
	stopped bool
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// start launches the server and waits until /healthz answers.
func start(t *testing.T, e env) *server {
	t.Helper()
	return startBinary(t, serverBin, e)
}

// startBinary is start with another server binary (an older build).
func startBinary(t *testing.T, bin string, e env) *server {
	t.Helper()
	addr := freeAddr(t)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "MFGRAPH_HTTP_ADDR="+addr)
	for k, v := range e {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	s := &server{t: t, cmd: cmd, base: "http://" + addr, logs: &syncBuffer{}, done: make(chan error, 1)}
	cmd.Stdout, cmd.Stderr = s.logs, s.logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { s.done <- cmd.Wait() }()
	t.Cleanup(func() {
		s.stop()
		if t.Failed() {
			t.Logf("server log:\n%s", s.logs.String())
		}
	})
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-s.done:
			t.Fatalf("server exited during startup: %v\n%s", err, s.logs.String())
		default:
		}
		resp, err := http.Get(s.base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return s
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server did not become healthy\n%s", s.logs.String())
	return nil
}

// stop sends SIGTERM and waits for the process to exit.
func (s *server) stop() {
	if s.stopped {
		return
	}
	s.stopped = true
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-s.done:
	case <-time.After(15 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.done
	}
}

// client talks to one server as one principal.
type client struct {
	t     *testing.T
	base  string
	token string
}

func (s *server) client(token string) *client { return &client{t: s.t, base: s.base, token: token} }

type response struct {
	status int
	header http.Header
	body   []byte
}

// code returns the "code" of an error envelope, or "".
func (r response) code() string {
	var e struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(r.body, &e)
	return e.Code
}

func (c *client) do(method, path string, body io.Reader, hdr map[string]string) response {
	c.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, c.base+path, body)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: b}
}

// json sends a JSON body (or none) and returns the response.
func (c *client) json(method, path string, in any, hdr map[string]string) response {
	c.t.Helper()
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			c.t.Fatal(err)
		}
		body = bytes.NewReader(b)
		if hdr == nil {
			hdr = map[string]string{}
		}
		hdr["Content-Type"] = "application/json"
	}
	return c.do(method, path, body, hdr)
}

// expect fails the test unless the response has the wanted status, and
// decodes a JSON body into out (if not nil). The failure message names the
// status and error code, e.g. "got 501 not_implemented".
func (c *client) expect(r response, what string, want int, out any) {
	c.t.Helper()
	if r.status != want {
		c.t.Fatalf("%s: got %d %s, want %d; body: %s", what, r.status, r.code(), want, truncate(r.body))
	}
	if out != nil {
		if err := json.Unmarshal(r.body, out); err != nil {
			c.t.Fatalf("%s: decode %s: %v", what, truncate(r.body), err)
		}
	}
}

func truncate(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}
