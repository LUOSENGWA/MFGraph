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
	uidA   = "01JD3X5Q8Z9W2V4T6R8P0N2MA0"
	uidB   = "01JD3X5Q8Z9W2V4T6R8P0N2MB0"
)

var serverBin string

func TestMain(m *testing.M) {
	if bin := os.Getenv("MFGRAPH_E2E_SERVER_BIN"); bin != "" {
		serverBin = bin
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "mfgraph-e2e-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	serverBin = filepath.Join(dir, "mfgraph-server")
	build := exec.Command("go", "build", "-o", serverBin, "github.com/LUOSENGWA/MFGraph/cmd/mfgraph-server")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build mfgraph-server:", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// env is the configuration of one server instance; restarting with the same
// env must find the same data.
type env map[string]string

// newEnv prepares storage (and, with MFGRAPH_E2E_DB_DSN, a fresh database).
func newEnv(t *testing.T) env {
	t.Helper()
	e := env{
		"MFGRAPH_AUTH_TOKENS":  tokenA + "=" + uidA + "," + tokenB + "=" + uidB,
		"MFGRAPH_STORAGE_ROOT": t.TempDir(),
		"MFGRAPH_LOG_FORMAT":   "text",
	}
	if dsn := os.Getenv("MFGRAPH_E2E_DB_DSN"); dsn != "" {
		cfg, err := mysql.ParseDSN(dsn)
		if err != nil {
			t.Fatal(err)
		}
		admin, err := sql.Open("mysql", cfg.FormatDSN())
		if err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("mfgraph_e2e_%d", time.Now().UnixNano())
		if _, err := admin.Exec("CREATE DATABASE " + name + " CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { admin.Exec("DROP DATABASE " + name); admin.Close() })
		cfg.DBName = name
		e["MFGRAPH_DB_DSN"] = cfg.FormatDSN()
		e["MFGRAPH_MIGRATE_ON_START"] = "true"
	}
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
	addr := freeAddr(t)
	cmd := exec.Command(serverBin)
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
