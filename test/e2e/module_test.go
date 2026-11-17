//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Module mode: the server runs as a module process of a host platform, on a
// Unix domain socket, behind identity headers, writing its events to the
// host's outbox table. The tests use a generic header prefix; the host's own
// settings are tested by the acceptance script of its packaging.
const (
	modPrefix  = "X-E2E-"
	modToken   = "e2e-internal-token-0123456789"
	modTenant  = "01JD3X5Q8Z9W2V4T6R8P0N2T00"
	projP      = "01JD3X5Q8Z9W2V4T6R8P0N2P01"
	projQ      = "01JD3X5Q8Z9W2V4T6R8P0N2P02"
	modRoot    = "http://module"
	modBase    = modRoot + "/internal/v1/mfgraph"
	hostOutbox = `CREATE TABLE IF NOT EXISTS module_outbox (
  id BIGINT AUTO_INCREMENT NOT NULL PRIMARY KEY,
  event_id VARCHAR(64) NOT NULL,
  module VARCHAR(64) NOT NULL,
  event_kind VARCHAR(64) NOT NULL,
  scope_kind VARCHAR(16) NOT NULL,
  scope_id VARCHAR(191) NOT NULL,
  actor_type VARCHAR(16) NULL,
  actor_id VARCHAR(255) NULL,
  payload_json JSON NOT NULL,
  status VARCHAR(16) NOT NULL DEFAULT 'pending',
  attempts INT NOT NULL DEFAULT 0,
  available_at DATETIME(6) NOT NULL,
  published_at DATETIME(6) NULL,
  last_error VARCHAR(1024) NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  UNIQUE KEY uk_module_outbox_event (event_id),
  KEY idx_module_outbox_pending (status, available_at),
  KEY idx_module_outbox_scope (scope_kind, scope_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`
)

// moduleEnv is one module deployment: its schema, the host's ops schema
// with the outbox table, a configuration file, a socket and the
// environment the host would inject.
type moduleEnv struct {
	t       *testing.T
	env     env
	config  string
	sock    string
	db, ops *sql.DB
	opsName string
}

func newModuleEnv(t *testing.T) *moduleEnv {
	t.Helper()
	e := newEnv(t) // a fresh schema (and the skip without a database)
	cfg, err := mysql.ParseDSN(e["MFGRAPH_DB_DSN"])
	if err != nil {
		t.Fatal(err)
	}
	m := &moduleEnv{t: t, opsName: cfg.DBName + "_ops"}
	admin := cfg.Clone()
	admin.DBName = ""
	adb, err := sql.Open("mysql", admin.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { adb.Exec("DROP DATABASE IF EXISTS " + m.opsName); adb.Close() })
	if _, err := adb.Exec("CREATE DATABASE " + m.opsName); err != nil {
		t.Fatal(err)
	}
	open := func(name string) *sql.DB {
		c := cfg.Clone()
		c.DBName = name
		d, err := sql.Open("mysql", c.FormatDSN())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		return d
	}
	m.db, m.ops = open(cfg.DBName), open(m.opsName)
	if _, err := m.ops.Exec(hostOutbox); err != nil {
		t.Fatalf("host outbox table: %v", err)
	}
	ops := cfg.Clone()
	ops.DBName = m.opsName
	// Socket paths are limited to about 100 bytes.
	dir, err := os.MkdirTemp("", "mfgm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m.sock = filepath.Join(dir, "m.sock")
	m.config = filepath.Join(t.TempDir(), "module.env")
	writeFile(t, m.config, "# module settings of the e2e tests\n"+
		"MFGRAPH_MODE=module\n"+
		"MFGRAPH_TRUSTED_HEADER_PREFIX="+modPrefix+"\n"+
		"MFGRAPH_INTERNAL_TOKEN_HEADER="+modPrefix+"Internal-Token\n"+
		"MFGRAPH_ROUTE_PREFIX=/internal/v1/mfgraph\n"+
		"MFGRAPH_OUTBOX_TABLE="+m.opsName+".module_outbox\n"+
		"MFGRAPH_KERNEL_MIGRATIONS=false\n"+
		"MFGRAPH_LOG_LEVEL=info\n")
	m.env = env{
		"MODULE_ID":                 "mfgraph",
		"MODULE_SOCKET":             m.sock,
		"MODULE_INTERNAL_TOKEN":     modToken,
		"MFGRAPH_DSN":               e["MFGRAPH_DB_DSN"],
		"MFGRAPH_OPS_DSN":           ops.FormatDSN(),
		"MFGRAPH_DATA_DIR":          e["MFGRAPH_STORAGE_ROOT"],
		"MFGRAPH_TENANT_ID":         modTenant,
		"MFGRAPH_LOG_PSEUDONYM_KEY": "e2e-pseudonym-key-0123456789",
	}
	return m
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// command returns the server command with the module environment, changed
// by override (an empty value removes a variable).
func (m *moduleEnv) command(override env, args ...string) *exec.Cmd {
	return m.commandContext(context.Background(), override, args...)
}

func (m *moduleEnv) commandContext(ctx context.Context, override env, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, serverBin, args...)
	vars := env{}
	for k, v := range m.env {
		vars[k] = v
	}
	for k, v := range override {
		if v == "" {
			delete(vars, k)
		} else {
			vars[k] = v
		}
	}
	cmd.Env = os.Environ()
	for k, v := range vars {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return cmd
}

// migrate runs "mfgraph-server --config FILE migrate".
func (m *moduleEnv) migrate() string {
	m.t.Helper()
	out, err := m.command(nil, "--config", m.config, "migrate").CombinedOutput()
	if err != nil {
		m.t.Fatalf("migrate: %v\n%s", err, out)
	}
	return string(out)
}

type moduleServer struct {
	t      *testing.T
	m      *moduleEnv
	cmd    *exec.Cmd
	logs   *syncBuffer
	done   chan error
	http   *http.Client
	exited bool
	err    error
}

// start starts "mfgraph-server serve --config FILE --socket PATH" (the
// tests run the migration command first, as the host does); it returns once
// /readyz answers 200.
func (m *moduleEnv) start(override env) *moduleServer {
	m.t.Helper()
	return m.startUntil(override, http.StatusOK)
}

// startAnswering is start without the wait for readiness: it returns once
// /readyz answers at all.
func (m *moduleEnv) startAnswering(override env) *moduleServer {
	m.t.Helper()
	return m.startUntil(override, 0)
}

// startUntil starts the server and waits until /readyz answers with status
// want (any status for 0).
func (m *moduleEnv) startUntil(override env, want int) *moduleServer {
	m.t.Helper()
	s := &moduleServer{t: m.t, m: m, logs: &syncBuffer{}, done: make(chan error, 1)}
	s.cmd = m.command(override, "serve", "--config", m.config, "--socket", m.sock)
	s.cmd.Stdout, s.cmd.Stderr = s.logs, s.logs
	s.http = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", m.sock)
	}}}
	if err := s.cmd.Start(); err != nil {
		m.t.Fatal(err)
	}
	go func() { s.done <- s.cmd.Wait() }()
	m.t.Cleanup(func() {
		s.stop()
		if m.t.Failed() {
			m.t.Logf("module server log:\n%s", s.logs.String())
		}
	})
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		select {
		case err := <-s.done:
			m.t.Fatalf("module server exited during startup: %v\n%s", err, s.logs.String())
		default:
		}
		if r, err := s.http.Get(modRoot + "/readyz"); err == nil {
			r.Body.Close()
			if want == 0 || r.StatusCode == want {
				return s
			}
		}
	}
	m.t.Fatalf("module server did not become ready\n%s", s.logs.String())
	return nil
}

// stop sends SIGTERM and returns how long the process took to exit.
func (s *moduleServer) stop() time.Duration {
	if s.exited {
		return 0
	}
	s.exited = true
	start := time.Now()
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case s.err = <-s.done:
	case <-time.After(15 * time.Second):
		_ = s.cmd.Process.Kill()
		s.err = <-s.done
		s.err = errors.Join(errors.New("killed after 15 s"), s.err)
	}
	return time.Since(start)
}

// kill ends the process with SIGKILL, as a crash would.
func (s *moduleServer) kill() {
	if s.exited {
		return
	}
	s.exited = true
	_ = s.cmd.Process.Kill()
	s.err = <-s.done
}

// claims are the identity headers of a request.
type claims struct {
	token, actor, actorType, project, obo, oboType string
}

func defaultClaims(project string) claims {
	return claims{token: modToken, actor: "manager", actorType: "human", project: project}
}

func (c claims) headers() map[string]string {
	h := map[string]string{}
	for k, v := range map[string]string{"Internal-Token": c.token, "Actor": c.actor, "Actor-Type": c.actorType, "Project": c.project,
		"On-Behalf-Of": c.obo, "On-Behalf-Of-Type": c.oboType} {
		if v != "" {
			h[modPrefix+k] = v
		}
	}
	return h
}

// do sends a request with the identity headers of c.
func (s *moduleServer) do(c claims, method, url string, body io.Reader, hdr map[string]string) response {
	s.t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		s.t.Fatal(err)
	}
	for k, v := range c.headers() {
		req.Header.Set(k, v)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return response{status: resp.StatusCode, header: resp.Header, body: b}
}

func (s *moduleServer) json(c claims, method, path string, in any) response {
	s.t.Helper()
	var body io.Reader
	hdr := map[string]string{}
	if in != nil {
		b, _ := json.Marshal(in)
		body, hdr["Content-Type"] = bytes.NewReader(b), "application/json"
	}
	return s.do(c, method, modBase+path, body, hdr)
}

func (s *moduleServer) expect(r response, what string, want int, out any) {
	s.t.Helper()
	if r.status != want {
		s.t.Fatalf("%s: got %d %s, want %d; body: %s", what, r.status, r.code(), want, truncate(r.body))
	}
	if out != nil {
		if err := json.Unmarshal(r.body, out); err != nil {
			s.t.Fatalf("%s: %v", what, err)
		}
	}
}

// workspace creates a workspace in the project of c: its ID and main branch.
func (s *moduleServer) workspace(c claims) (string, string) {
	s.t.Helper()
	var ws struct {
		ID   string `json:"workspace_id"`
		Main string `json:"main_branch_id"`
	}
	s.expect(s.json(c, "POST", "/workspaces", map[string]string{"project_id": c.project, "display_name": "e2e"}), "POST workspaces", 201, &ws)
	return ws.ID, ws.Main
}

// upload stores content through the upload path /objects/{workspace_id}.
func (s *moduleServer) upload(c claims, ws, content string) string {
	s.t.Helper()
	var o struct {
		Hash string `json:"hash"`
	}
	r := s.do(c, "PUT", modBase+"/objects/"+ws, strings.NewReader(content), map[string]string{"Content-Type": "application/octet-stream"})
	if r.status != 200 && r.status != 201 {
		s.t.Fatalf("PUT /objects/%s: %d %s", ws, r.status, truncate(r.body))
	}
	_ = json.Unmarshal(r.body, &o)
	return o.Hash
}

func (s *moduleServer) commit(c claims, ws, branch string, generation int, key string, changes ...map[string]any) (response, string) {
	s.t.Helper()
	hdr := map[string]string{"Content-Type": "application/json"}
	if key != "" {
		hdr["Idempotency-Key"] = key
	}
	b, _ := json.Marshal(map[string]any{"branch_id": branch, "expected_generation": generation, "changes": changes})
	r := s.do(c, "POST", modBase+"/workspaces/"+ws+"/commits", bytes.NewReader(b), hdr)
	var res struct {
		ID string `json:"snapshot_id"`
	}
	_ = json.Unmarshal(r.body, &res)
	return r, res.ID
}

func putChange(path, hash string) map[string]any { return map[string]any{"path": path, "hash": hash} }

// hostRow is a row of the host's outbox table.
type hostRow struct {
	eventID, module, kind, scopeKind, scopeID string
	actorType, actorID                        sql.NullString
	payload, status                           string
	attempts                                  int
	createdAt, availableAt                    string
	env                                       map[string]any
}

// hostRows returns the rows of one workspace (all rows for ws ""), in the
// order of the table's ID.
func (m *moduleEnv) hostRows(ws string) []hostRow {
	m.t.Helper()
	rows, err := m.ops.Query(`SELECT event_id, module, event_kind, scope_kind, scope_id, actor_type, actor_id, payload_json, status,
		attempts, CAST(created_at AS CHAR), CAST(available_at AS CHAR) FROM module_outbox ORDER BY id`)
	if err != nil {
		m.t.Fatal(err)
	}
	defer rows.Close()
	var out []hostRow
	for rows.Next() {
		var r hostRow
		if err := rows.Scan(&r.eventID, &r.module, &r.kind, &r.scopeKind, &r.scopeID, &r.actorType, &r.actorID, &r.payload, &r.status,
			&r.attempts, &r.createdAt, &r.availableAt); err != nil {
			m.t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(r.payload), &r.env); err != nil {
			m.t.Fatalf("payload_json %s: %v", r.payload, err)
		}
		if data, _ := r.env["data"].(map[string]any); ws == "" || data["workspace_id"] == ws {
			out = append(out, r)
		}
	}
	return out
}

// waitHostRows waits until the host table has n rows of the workspace.
func (m *moduleEnv) waitHostRows(ws string, n int, within time.Duration) []hostRow {
	m.t.Helper()
	deadline := time.Now().Add(within)
	for {
		rows := m.hostRows(ws)
		if len(rows) >= n || time.Now().After(deadline) {
			if len(rows) != n {
				m.t.Fatalf("host table: %d rows of workspace %s after %v, want %d: %v", len(rows), ws, within, n, kindsOf(rows))
			}
			return rows
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func kindsOf(rows []hostRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, strings.TrimPrefix(r.kind, "mfgraph."))
	}
	return out
}

// registeredKinds reads the event kinds of docs/events.md.
func registeredKinds(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "events.md"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range regexp.MustCompile("(?m)^\\| `(mfgraph\\.[a-z_]+)` \\|").FindAllStringSubmatch(string(b), -1) {
		out = append(out, m[1])
	}
	return out
}

// tcpListeners counts the listening TCP sockets of a process: lsof, or
// /proc on Linux; -1 when neither can tell.
func tcpListeners(pid int) int {
	if path, err := exec.LookPath("lsof"); err == nil {
		out, _ := exec.Command(path, "-nP", "-a", "-p", strconv.Itoa(pid), "-iTCP", "-sTCP:LISTEN").Output()
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if strings.TrimSpace(string(out)) == "" {
			return 0
		}
		return len(lines) - 1 // header
	}
	if runtime.GOOS != "linux" {
		return -1
	}
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return -1
	}
	inodes := map[string]bool{}
	for _, fd := range fds {
		if l, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, fd.Name())); err == nil && strings.HasPrefix(l, "socket:[") {
			inodes[strings.TrimSuffix(strings.TrimPrefix(l, "socket:["), "]")] = true
		}
	}
	n := 0
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := os.ReadFile(table)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n")[1:] {
			if f := strings.Fields(line); len(f) > 9 && f[3] == "0A" && inodes[f[9]] {
				n++
			}
		}
	}
	return n
}

// ---- appendix B rows -------------------------------------------------------

// Row 6: migrate (with --config before the command) runs twice; the module's
// versions are recorded; the kernel tables are not created.
func TestModuleModeMigrate(t *testing.T) {
	m := newModuleEnv(t)
	first, second := m.migrate(), m.migrate()
	if !strings.Contains(first, `"component":"mfgraph"`) || strings.Contains(first+second, `"component":"kernel"`) ||
		!strings.Contains(second, `"applied":null,"skipped":7`) {
		t.Fatalf("migrate logs:\n%s\n%s", first, second)
	}
	var versions []string
	rows, err := m.db.Query("SELECT component, version FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var c, v string
		_ = rows.Scan(&c, &v)
		versions = append(versions, c+"/"+v)
	}
	rows.Close()
	want := []string{"mfgraph/0001_workspaces", "mfgraph/0002_workstreams_branches", "mfgraph/0003_snapshots", "mfgraph/0004_blobs",
		"mfgraph/0006_uploads", "mfgraph/0008_usage_outbox_idempotency", "mfgraph/0009_actor_types"}
	if strings.Join(versions, ",") != strings.Join(want, ",") {
		t.Fatalf("recorded versions %v", versions)
	}
	var kernel int
	if err := m.db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE()
		AND table_name IN ('projects', 'principals')`).Scan(&kernel); err != nil || kernel != 0 {
		t.Fatalf("kernel tables: %d %v", kernel, err)
	}
	// The order of the options does not matter.
	if out, err := m.command(nil, "migrate", "--config", m.config).CombinedOutput(); err != nil || strings.Contains(string(out), "kernel") {
		t.Fatalf("migrate --config after the command: %v\n%s", err, out)
	}
}

// The host runs the migration command with MFGRAPH_DSN alone; the rest
// comes from the bundle's etc/module.env (here the file of the packaging
// directory, which the bundle carries unchanged): no socket, internal
// token, tenant, data directory or ops DSN, and nothing else in the
// environment. Only the module's migrations run, and a second run skips
// them.
func TestModuleModeMigrateOnlyDSN(t *testing.T) {
	m := newModuleEnv(t)
	moduleEnvFile, err := filepath.Abs(filepath.Join("..", "..", "packaging", "host-module", "module.env"))
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{`"component":"mfgraph","applied":["0001_workspaces",`, `"component":"mfgraph","applied":null,"skipped":7`} {
		cmd := exec.Command(serverBin, "--config", moduleEnvFile, "migrate")
		cmd.Env = []string{"MFGRAPH_DSN=" + m.env["MFGRAPH_DSN"]}
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), want) || strings.Contains(string(out), `"component":"kernel"`) ||
			strings.Contains(string(out), `"level":"WARN"`) || strings.Contains(string(out), `"level":"ERROR"`) {
			t.Fatalf("migrate %d with MFGRAPH_DSN only: %v\n%s", i+1, err, out)
		}
	}
	var mfgraph, others, kernel int
	if err := m.db.QueryRow(`SELECT SUM(component = 'mfgraph'), SUM(component <> 'mfgraph') FROM schema_migrations`).Scan(&mfgraph,
		&others); err != nil || mfgraph != 7 || others != 0 {
		t.Fatalf("recorded versions: mfgraph %d, others %d, %v", mfgraph, others, err)
	}
	if err := m.db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE()
		AND table_name IN ('projects', 'principals')`).Scan(&kernel); err != nil || kernel != 0 {
		t.Fatalf("kernel tables: %d %v", kernel, err)
	}
}

// Row 1: readyz, the socket (mode 0600, a stale one replaced), no TCP port.
func TestModuleModeStartup(t *testing.T) {
	m := newModuleEnv(t)
	m.migrate()
	stale, err := net.Listen("unix", m.sock)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()
	s := m.start(nil)
	r, err := s.http.Get(modRoot + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	var ready struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	_ = json.NewDecoder(r.Body).Decode(&ready)
	r.Body.Close()
	if ready.Status != "ready" || ready.Checks["db"] != "ok" || ready.Checks["storage"] != "ok" || ready.Checks["outbox"] != "ok" {
		t.Fatalf("readyz: %+v", ready)
	}
	fi, err := os.Stat(m.sock)
	if err != nil || fi.Mode().Perm() != 0o600 || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket: %v %v", fi.Mode(), err)
	}
	if !strings.Contains(s.logs.String(), `"msg":"listening","addr":"`+m.sock+`"`) {
		t.Fatalf("listening line:\n%s", s.logs.String())
	}
	switch n := tcpListeners(s.cmd.Process.Pid); n {
	case 0:
	case -1:
		t.Log("TCP listeners not measured (no lsof and no /proc)")
	default:
		t.Fatalf("the module process listens on %d TCP sockets", n)
	}
	// The host's outbox table gone: not ready.
	if _, err := m.ops.Exec("RENAME TABLE module_outbox TO module_outbox_away"); err != nil {
		t.Fatal(err)
	}
	r, _ = s.http.Get(modRoot + "/readyz")
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 503 || !strings.Contains(string(b), `"check":"outbox"`) {
		t.Fatalf("readyz without the host table: %d %s", r.StatusCode, b)
	}
	if _, err := m.ops.Exec("RENAME TABLE module_outbox_away TO module_outbox"); err != nil {
		t.Fatal(err)
	}
}

// Rows 2 and 3: the module prefix, /api/v1 404, the upload path, and the
// identity headers: 401 without them, the actor recorded with them.
func TestModuleModePrefixAndIdentity(t *testing.T) {
	m := newModuleEnv(t)
	m.migrate()
	s := m.start(nil)
	actor := "worker-" + strings.Repeat("w", 253-len("worker-")) // the longest canonical reference
	c := claims{token: modToken, actor: actor, actorType: "worker", project: projP, obo: "manager", oboType: "human"}
	ws, main := s.workspace(c)
	h := s.upload(c, ws, "hello")
	r, snap := s.commit(c, ws, main, 1, "", putChange("f.txt", h))
	s.expect(r, "commit", 201, nil)
	var raw map[string]any
	s.expect(s.json(c, "GET", "/snapshots/"+snap, nil), "GET snapshot", 200, &raw)
	if raw["author_ref"] != actor || raw["author_type"] != "worker" || raw["on_behalf_of_ref"] != "manager" ||
		raw["on_behalf_of_type"] != "human" || raw["author_principal_uid"] != nil {
		t.Fatalf("snapshot actors: %v", raw)
	}
	r = s.do(c, "GET", modRoot+"/api/v1/snapshots/"+snap, nil, nil)
	if r.status != 404 || r.code() != "not_found" {
		t.Fatalf("/api/v1 in module mode: %d %s", r.status, r.code())
	}
	// Paths below the prefix that are not in their clean form (the review's
	// examples): 404, never a redirect to /api/v1/...
	noRedirect := *s.http
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for _, p := range []string{"//projects", "/x/../workspaces/" + ws, "/workspaces/" + ws + "/"} {
		req, _ := http.NewRequest("GET", modBase+p, nil)
		for k, v := range c.headers() {
			req.Header.Set(k, v)
		}
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 || resp.Header.Get("Location") != "" {
			t.Fatalf("%s: %d, Location %q", p, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	// The request ID of the host's proxy.
	r = s.do(c, "GET", modBase+"/workspaces/"+ws, nil, map[string]string{modPrefix + "Request-Id": "host-request-1"})
	if r.status != 200 || r.header.Get("X-Request-Id") != "host-request-1" {
		t.Fatalf("request ID: %d %q", r.status, r.header.Get("X-Request-Id"))
	}
	ok := defaultClaims(projP)
	for name, cl := range map[string]claims{
		"no token":         {actor: "manager", actorType: "human", project: projP},
		"wrong token":      {token: modToken + "x", actor: "manager", actorType: "human", project: projP},
		"no actor":         {token: modToken, actorType: "human", project: projP},
		"no actor type":    {token: modToken, actor: "manager", project: projP},
		"no project":       {token: modToken, actor: "manager", actorType: "human"},
		"actor not UTF-8":  {token: modToken, actor: "man\xffager", actorType: "human", project: projP},
		"actor 256 bytes":  {token: modToken, actor: strings.Repeat("a", 256), actorType: "human", project: projP},
		"bad actor type":   {token: modToken, actor: "manager", actorType: "Human", project: projP},
		"project not ULID": {token: modToken, actor: "manager", actorType: "human", project: "project-1"},
	} {
		r := s.do(cl, "GET", modBase+"/workspaces/"+ws, nil, nil)
		if r.status != 401 || r.code() != "unauthenticated" || r.header.Get("WWW-Authenticate") != "" {
			t.Errorf("%s: %d %s", name, r.status, truncate(r.body))
		}
	}
	// A bearer token is no substitute for the identity headers.
	r = s.do(claims{}, "GET", modBase+"/workspaces/"+ws, nil, map[string]string{"Authorization": "Bearer " + modToken})
	if r.status != 401 {
		t.Fatalf("bearer token in module mode: %d", r.status)
	}
	if r := s.do(ok, "GET", modBase+"/workspaces/"+ws, nil, nil); r.status != 200 {
		t.Fatalf("valid headers: %d", r.status)
	}
	if strings.Contains(s.logs.String(), modToken) {
		t.Fatal("the log contains the internal token")
	}
}

// Row 4: resources of another project are 404, exactly like unknown IDs;
// /projects does not exist.
func TestModuleModeACL(t *testing.T) {
	m := newModuleEnv(t)
	m.migrate()
	s := m.start(nil)
	cp, cq := defaultClaims(projP), defaultClaims(projQ)
	ws, main := s.workspace(cp)
	h := s.upload(cp, ws, "hello")
	r, snap := s.commit(cp, ws, main, 1, "", putChange("f.txt", h))
	s.expect(r, "commit", 201, nil)
	const unknown = "01JD3X5Q8Z9W2V4T6R8P0N2MZZ"
	type req struct{ method, path, body string }
	reqs := func(ws, snap string) []req {
		return []req{
			{"GET", "/workspaces/" + ws, ""}, {"GET", "/workspaces/" + ws + "/branches", ""},
			{"GET", "/workspaces/" + ws + "/timeline", ""}, {"PUT", "/workspaces/" + ws + "/objects", "x"},
			{"PUT", "/objects/" + ws, "x"},
			{"POST", "/workspaces/" + ws + "/commits", `{"branch_id":"` + main + `","expected_generation":2,"changes":[{"path":"f.txt","delete":true}]}`},
			{"GET", "/snapshots/" + snap, ""}, {"GET", "/snapshots/" + snap + "/tree", ""},
			{"GET", "/snapshots/" + snap + "/file?path=f.txt", ""}, {"GET", "/snapshots/" + snap + "/diff", ""},
			{"POST", "/snapshots/" + snap + "/restore", `{"branch_id":"` + main + `","expected_generation":2}`},
		}
	}
	send := func(c claims, q req) response {
		hdr := map[string]string{"Content-Type": "application/json"}
		if q.method == "PUT" {
			hdr["Content-Type"] = "application/octet-stream"
		}
		var body io.Reader
		if q.body != "" {
			body = strings.NewReader(q.body)
		}
		return s.do(c, q.method, modBase+q.path, body, hdr)
	}
	strip := func(b []byte) string {
		var e map[string]any
		_ = json.Unmarshal(b, &e)
		delete(e, "request_id")
		out, _ := json.Marshal(e)
		return string(out)
	}
	real, missing := reqs(ws, snap), reqs(unknown, unknown)
	for i := range real {
		a, b := send(cq, real[i]), send(cq, missing[i])
		if a.status != 404 || a.code() != "not_found" || b.status != 404 || strip(a.body) != strip(b.body) {
			t.Errorf("%s %s from another project: %d %s; unknown ID: %d %s", real[i].method, real[i].path, a.status, a.body,
				b.status, b.body)
		}
	}
	if r := s.json(cq, "POST", "/workspaces", map[string]string{"project_id": projP}); r.status != 404 {
		t.Fatalf("workspace in another project: %d", r.status)
	}
	for _, q := range []req{{"GET", "/projects/" + projP, ""}, {"POST", "/projects", `{"name":"p"}`}, {"GET", "/projects", ""}} {
		if r := send(cp, q); r.status != 404 || r.code() != "not_found" {
			t.Errorf("%s %s: %d %s", q.method, q.path, r.status, truncate(r.body))
		}
	}
}

// Row 7 and the checks of the host table: kinds, columns, envelopes,
// reasons, order, a 255-byte actor, rollback, replay, redelivery, a table
// that is gone for a while; the drift check against docs/events.md.
func TestModuleModeEvents(t *testing.T) {
	m := newModuleEnv(t)
	m.migrate()
	s := m.start(nil)
	actor := "@" + strings.Repeat("u", 255-len("@:example.com")) + ":example.com" // 255 bytes
	c := claims{token: modToken, actor: actor, actorType: "worker", project: projP}
	ws, main := s.workspace(c)
	h1 := s.upload(c, ws, "one")
	r, s1 := s.commit(c, ws, main, 1, "k-1", putChange("a.txt", h1))
	s.expect(r, "commit 1", 201, nil)
	// Rolled back: a stale generation and content that was never uploaded.
	if r, _ := s.commit(c, ws, main, 1, "", putChange("b.txt", h1)); r.status != 409 {
		t.Fatalf("stale commit: %d", r.status)
	}
	if r, _ := s.commit(c, ws, main, 2, "", putChange("b.txt", sha("never uploaded"))); r.status != 410 {
		t.Fatalf("commit of content not uploaded: %d", r.status)
	}
	h2 := s.upload(c, ws, "two")
	r, s2 := s.commit(c, ws, main, 2, "", putChange("b.txt", h2))
	s.expect(r, "commit 2", 201, nil)
	var restored struct {
		ID string `json:"snapshot_id"`
	}
	s.expect(s.json(c, "POST", "/snapshots/"+s1+"/restore", map[string]any{"branch_id": main, "expected_generation": 3}), "restore", 201, &restored)
	rows := m.waitHostRows(ws, 8, 10*time.Second)
	want := []string{"workspace_created", "branch_created", "commit_created", "branch_updated", "commit_created", "branch_updated",
		"snapshot_restored", "branch_updated"}
	if strings.Join(kindsOf(rows), ",") != strings.Join(want, ",") {
		t.Fatalf("host rows in order: %v", kindsOf(rows))
	}
	keys := []string{ws, main, s1, main, s2, main, restored.ID, main}
	versions := []int{1, 1, 1, 2, 1, 3, 1, 4}
	reasons := map[int]string{3: "commit", 5: "commit", 7: "restore"}
	for i, row := range rows {
		sum := sha256.Sum256([]byte(fmt.Sprintf("mfgraph\n%s\n%s\n%s\n%d", row.kind, projP, keys[i], versions[i])))
		actorObj, _ := row.env["actor"].(map[string]any)
		data, _ := row.env["data"].(map[string]any)
		occurred, _ := row.env["occurred_at"].(string)
		at, err := time.Parse(time.RFC3339Nano, occurred)
		created, cerr := time.ParseInLocation("2006-01-02 15:04:05.000000", row.createdAt, time.UTC)
		if row.eventID != hex.EncodeToString(sum[:]) || row.env["event_id"] != row.eventID || row.env["v"] != float64(1) ||
			row.module != "mfgraph" || row.scopeKind != "project" || row.scopeID != projP || row.env["project_id"] != projP ||
			row.actorType.String != "worker" || row.actorID.String != actor || actorObj["id"] != actor || actorObj["type"] != "worker" ||
			row.status != "pending" || row.attempts != 0 || err != nil || cerr != nil || !created.Equal(at) {
			t.Errorf("row %d (%s): %+v", i, row.kind, row)
		}
		if reason, ok := reasons[i]; ok && data["reason"] != reason {
			t.Errorf("row %d: reason %v, want %s", i, data["reason"], reason)
		}
	}
	// Drift check: every kind the host table holds is registered in
	// docs/events.md, and this version emits exactly the five kinds.
	registered := registeredKinds(t)
	emitted := map[string]bool{}
	for _, row := range m.hostRows("") {
		emitted[row.kind] = true
		if !slices.Contains(registered, row.kind) {
			t.Errorf("event kind %s is not in docs/events.md", row.kind)
		}
		// The tenant is internal to the module: never in an envelope.
		if strings.Contains(row.payload, modTenant) || strings.Contains(strings.ToLower(row.payload), "tenant") {
			t.Errorf("%s: the envelope carries the tenant: %s", row.kind, row.payload)
		}
	}
	var kinds []string
	for k := range emitted {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	if strings.Join(kinds, ",") != "mfgraph.branch_created,mfgraph.branch_updated,mfgraph.commit_created,mfgraph.snapshot_restored,"+
		"mfgraph.workspace_created" || len(registered) != 6 || !slices.Contains(registered, "mfgraph.snapshot_created") {
		t.Fatalf("emitted %v, registered %v", kinds, registered)
	}

	// A replayed commit emits nothing; redelivered events are absorbed.
	if r, id := s.commit(c, ws, main, 1, "k-1", putChange("a.txt", h1)); r.status != 201 || id != s1 {
		t.Fatalf("replay: %d %s", r.status, id)
	}
	if _, err := m.db.Exec("UPDATE outbox_events SET status = 'pending', published_at_us = NULL"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		var pending int
		_ = m.db.QueryRow("SELECT COUNT(*) FROM outbox_events WHERE status = 'pending'").Scan(&pending)
		if pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d events not redelivered", pending)
		}
	}
	if n := len(m.hostRows(ws)); n != 8 {
		t.Fatalf("after replay and redelivery: %d host rows", n)
	}

	// The host table is gone for a while: the events wait in order and
	// arrive once it is back.
	if _, err := m.ops.Exec("RENAME TABLE module_outbox TO module_outbox_away"); err != nil {
		t.Fatal(err)
	}
	h3 := s.upload(c, ws, "three")
	r, s3 := s.commit(c, ws, main, 4, "", putChange("c.txt", h3))
	s.expect(r, "commit 3", 201, nil)
	time.Sleep(1500 * time.Millisecond)
	var retries int
	if err := m.db.QueryRow(`SELECT retry_count FROM outbox_events WHERE status = 'pending' AND aggregate_id = ?`, s3).Scan(&retries); err != nil ||
		retries < 1 {
		t.Fatalf("retries of the undeliverable event: %d %v", retries, err)
	}
	if _, err := m.ops.Exec("RENAME TABLE module_outbox_away TO module_outbox"); err != nil {
		t.Fatal(err)
	}
	rows = m.waitHostRows(ws, 10, 20*time.Second)
	if got := strings.Join(kindsOf(rows[8:]), ","); got != "commit_created,branch_updated" {
		t.Fatalf("late rows: %s", got)
	}
	if d, _ := rows[8].env["data"].(map[string]any); d["snapshot_id"] != s3 {
		t.Fatalf("late commit row: %v", rows[8].env)
	}
}

// The forwarder's connection is always utf8mb4: an actor with Chinese
// characters and an emoji arrives unchanged in the host table (with
// charset=utf8 it failed on MySQL for every attempt, and latin1 mangled it).
func TestModuleModeUnicodeActor(t *testing.T) {
	m := newModuleEnv(t)
	m.migrate()
	s := m.start(nil)
	const actor = "\u5f20\u4e09-\U0001F600" // 张三-😀
	c := claims{token: modToken, actor: actor, actorType: "human", project: projP}
	ws, main := s.workspace(c)
	r, _ := s.commit(c, ws, main, 1, "", putChange("u.txt", s.upload(c, ws, "unicode")))
	s.expect(r, "commit", 201, nil)
	for _, row := range m.waitHostRows(ws, 4, 10*time.Second) {
		a, _ := row.env["actor"].(map[string]any)
		if row.actorID.String != actor || a["id"] != actor {
			t.Fatalf("%s: actor_id %q, envelope actor %v, want %q", row.kind, row.actorID.String, a["id"], actor)
		}
	}
}

// A stalled forwarder is visible: the host table is too narrow for the
// actor (the first DDL of the host's table had actor_id VARCHAR(191)), the
// event at the head of the queue fails again and again, and after 5
// failures /readyz still answers 200 but reports checks.outbox "stalled"
// with the backlog and the error, and the log has an ERROR line. Repairing
// the table lets the events through (here with the last delivery of the
// shutdown), with an INFO line.
func TestModuleModeStalledForwarder(t *testing.T) {
	m := newModuleEnv(t)
	m.migrate()
	if _, err := m.ops.Exec("ALTER TABLE module_outbox MODIFY actor_id VARCHAR(32) NULL"); err != nil {
		t.Fatal(err)
	}
	s := m.start(nil)
	c := claims{token: modToken, actor: "agent-" + strings.Repeat("x", 34), actorType: "worker", project: projP} // 40 bytes
	ws, _ := s.workspace(c)
	type readiness struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
		Outbox map[string]any    `json:"outbox"`
	}
	var ready readiness
	var code int
	for deadline := time.Now().Add(45 * time.Second); ; time.Sleep(500 * time.Millisecond) {
		r := s.do(claims{}, "GET", modRoot+"/readyz", nil, nil)
		code = r.status
		_ = json.Unmarshal(r.body, &ready)
		if ready.Checks["outbox"] == "stalled" || time.Now().After(deadline) {
			break
		}
	}
	lastErr, _ := ready.Outbox["last_error"].(string)
	if code != 200 || ready.Status != "ready" || ready.Checks["outbox"] != "stalled" || ready.Outbox["pending"] != float64(2) ||
		ready.Outbox["stalled_since"] == nil || ready.Outbox["oldest_pending_at"] == nil || !strings.Contains(lastErr, "1406") {
		t.Fatalf("readyz while stalled: %d %+v", code, ready)
	}
	var stalledLine string
	for _, line := range strings.Split(s.logs.String(), "\n") {
		if strings.Contains(line, `"level":"ERROR"`) && strings.Contains(line, "is stalled") {
			stalledLine = line
		}
	}
	for _, want := range []string{`"pending":2`, `"oldest_pending_at":`, `"event_id":"`, `"event_kind":"mfgraph.workspace_created"`,
		`"failures":5`, "1406"} {
		if !strings.Contains(stalledLine, want) {
			t.Fatalf("the ERROR line lacks %s:\n%s", want, s.logs.String())
		}
	}
	if strings.Contains(s.logs.String(), "mfgraph:") || strings.Contains(s.logs.String(), "@tcp(") {
		t.Fatalf("the log shows a DSN:\n%s", s.logs.String())
	}
	if _, err := m.ops.Exec("ALTER TABLE module_outbox MODIFY actor_id VARCHAR(255) NULL"); err != nil {
		t.Fatal(err)
	}
	if took := s.stop(); s.err != nil || took > 8*time.Second {
		t.Fatalf("stop: %v after %v", s.err, took)
	}
	if rows := m.hostRows(ws); len(rows) != 2 || rows[0].actorID.String != c.actor {
		t.Fatalf("host rows after the repair: %v", kindsOf(rows))
	}
	if !strings.Contains(s.logs.String(), "delivery to the host's outbox table resumed") {
		t.Fatalf("no INFO line on recovery:\n%s", s.logs.String())
	}
}

// The host's unique key on event_id has another name than in its
// migration. The process is killed (SIGKILL) after the events were written
// to the host table, and the local outbox is set back to pending, as if the
// marks had been lost with the process. After the restart the events are
// delivered again and recognized as delivered by the duplicate event_id:
// nothing is added, nothing stays pending. A table without a unique index
// on event_id gives an ERROR line at the start, and the module still runs.
func TestModuleModeRedeliveryAnyKeyName(t *testing.T) {
	m := newModuleEnv(t)
	m.migrate()
	for _, stmt := range []string{"ALTER TABLE module_outbox DROP INDEX uk_module_outbox_event",
		"ALTER TABLE module_outbox ADD UNIQUE KEY uk_event_id (event_id)"} {
		if _, err := m.ops.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	s := m.start(nil)
	c := defaultClaims(projP)
	ws, main := s.workspace(c)
	r, _ := s.commit(c, ws, main, 1, "", putChange("r.txt", s.upload(c, ws, "redelivered")))
	s.expect(r, "commit", 201, nil)
	m.waitHostRows(ws, 4, 10*time.Second)
	s.kill()
	if _, err := m.db.Exec("UPDATE outbox_events SET status = 'pending', published_at_us = NULL"); err != nil {
		t.Fatal(err)
	}
	s2 := m.start(nil)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		var pending int
		_ = m.db.QueryRow("SELECT COUNT(*) FROM outbox_events WHERE status = 'pending'").Scan(&pending)
		if pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d events still pending after the restart:\n%s", pending, s2.logs.String())
		}
	}
	if rows := m.hostRows(ws); len(rows) != 4 {
		t.Fatalf("%d host rows after the redelivery, want 4", len(rows))
	}
	if strings.Contains(s2.logs.String(), `"level":"ERROR"`) {
		t.Fatalf("an ERROR line with the unique key uk_event_id:\n%s", s2.logs.String())
	}
	s2.stop()

	// No unique index on event_id: an ERROR line, and the module runs.
	if _, err := m.ops.Exec("ALTER TABLE module_outbox DROP INDEX uk_event_id"); err != nil {
		t.Fatal(err)
	}
	s3 := m.start(nil)
	if !strings.Contains(s3.logs.String(), `"level":"ERROR","msg":"outbox: the host's outbox table has no unique index on event_id`) {
		t.Fatalf("no ERROR line for the missing unique index:\n%s", s3.logs.String())
	}
	if r := s3.do(claims{}, "GET", modRoot+"/readyz", nil, nil); r.status != 200 {
		t.Fatalf("readyz without the unique index: %d %s", r.status, r.body)
	}
}

// Row 8: SIGTERM ends the process within 8 s and removes the socket; the
// data is there after a restart, and events that could not be delivered
// before the stop are delivered after it.
func TestModuleModeSIGTERM(t *testing.T) {
	m := newModuleEnv(t)
	m.migrate()
	s := m.start(nil)
	c := defaultClaims(projP)
	ws, main := s.workspace(c)
	h := s.upload(c, ws, "kept")
	r, snap := s.commit(c, ws, main, 1, "", putChange("k.txt", h))
	s.expect(r, "commit", 201, nil)
	m.waitHostRows(ws, 4, 10*time.Second)
	// Undeliverable at the stop.
	if _, err := m.ops.Exec("RENAME TABLE module_outbox TO module_outbox_away"); err != nil {
		t.Fatal(err)
	}
	r, _ = s.commit(c, ws, main, 2, "", map[string]any{"path": "k.txt", "delete": true})
	s.expect(r, "commit 2", 201, nil)
	took := s.stop()
	if s.err != nil || took > 8*time.Second {
		t.Fatalf("SIGTERM: exit %v after %v", s.err, took)
	}
	if _, err := os.Lstat(m.sock); !os.IsNotExist(err) {
		t.Fatalf("socket after the stop: %v", err)
	}
	var pending int
	if err := m.db.QueryRow("SELECT COUNT(*) FROM outbox_events WHERE status = 'pending'").Scan(&pending); err != nil || pending != 2 {
		t.Fatalf("pending after the stop: %d %v", pending, err)
	}
	if _, err := m.ops.Exec("RENAME TABLE module_outbox_away TO module_outbox"); err != nil {
		t.Fatal(err)
	}
	s2 := m.start(nil)
	var tree struct {
		Items []struct {
			Path string `json:"path"`
		} `json:"items"`
	}
	s2.expect(s2.json(c, "GET", "/snapshots/"+snap+"/tree", nil), "tree after the restart", 200, &tree)
	if len(tree.Items) != 1 || tree.Items[0].Path != "k.txt" {
		t.Fatalf("tree after the restart: %+v", tree)
	}
	rows := m.waitHostRows(ws, 6, 10*time.Second)
	if got := strings.Join(kindsOf(rows[4:]), ","); got != "commit_created,branch_updated" {
		t.Fatalf("events delivered after the restart: %s", got)
	}
	t.Logf("measured: exit after %v; %d events delivered after the restart", took.Round(time.Millisecond), 2)
}

// silentPort accepts TCP connections and never answers, like a database
// that is stuck (or a port that is not a database at all).
func silentPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	return ln.Addr().String()
}

// freezableProxy forwards TCP connections to target until freeze is called;
// from then on it forwards nothing and keeps the connections open, like a
// database that stopped answering (docker pause).
func freezableProxy(t *testing.T, target string) (addr string, freeze func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	frozen := make(chan struct{})
	var conns []net.Conn
	pump := func(dst, src net.Conn) {
		buf := make([]byte, 32<<10)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				select {
				case <-frozen:
					<-make(chan struct{}) // hold the data and the connection
				default:
				}
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", target)
			if err != nil {
				c.Close()
				continue
			}
			mu.Lock()
			conns = append(conns, c, up)
			mu.Unlock()
			go pump(up, c)
			go pump(c, up)
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	var once sync.Once
	return ln.Addr().String(), func() { once.Do(func() { close(frozen) }) }
}

// SIGTERM ends a module within 8 s even when a database does not answer:
// the forwarder's connection to the host table hangs (the review measured
// 8.01 s), or the module's own database stops answering while a request
// waits for it. Module mode finishes within 7 s and leaves 1 s for the
// exit.
func TestModuleModeSIGTERMUnresponsive(t *testing.T) {
	t.Run("ops database silent", func(t *testing.T) {
		m := newModuleEnv(t)
		m.migrate()
		s := m.startAnswering(env{"MFGRAPH_OPS_DSN": "ops:ops-pass@tcp(" + silentPort(t) + ")/ops"})
		c := defaultClaims(projP)
		ws, main := s.workspace(c)
		r, _ := s.commit(c, ws, main, 1, "", putChange("s.txt", s.upload(c, ws, "silent")))
		s.expect(r, "commit", 201, nil)
		time.Sleep(time.Second) // the forwarder waits for the silent database
		took := s.stop()
		if took > 8*time.Second || s.err != nil && strings.Contains(s.err.Error(), "killed") {
			t.Fatalf("SIGTERM with a silent ops database: exit %v after %v\n%s", s.err, took, s.logs.String())
		}
		var pending int
		if err := m.db.QueryRow("SELECT COUNT(*) FROM outbox_events WHERE status = 'pending'").Scan(&pending); err != nil || pending != 4 {
			t.Fatalf("pending after the stop: %d %v", pending, err)
		}
		t.Logf("measured: exit %v after %v with a silent ops database", s.err, took.Round(time.Millisecond))
	})
	t.Run("database frozen during a request", func(t *testing.T) {
		m := newModuleEnv(t)
		m.migrate()
		cfg, err := mysql.ParseDSN(m.env["MFGRAPH_DSN"])
		if err != nil {
			t.Fatal(err)
		}
		proxy, freeze := freezableProxy(t, cfg.Addr)
		cfg.Addr = proxy
		s := m.start(env{"MFGRAPH_DSN": cfg.FormatDSN()})
		c := defaultClaims(projP)
		ws, _ := s.workspace(c)
		freeze()
		go func() { // waits for the frozen database until the process ends
			req, _ := http.NewRequest("GET", modBase+"/workspaces/"+ws, nil)
			for k, v := range c.headers() {
				req.Header.Set(k, v)
			}
			if resp, err := s.http.Do(req); err == nil {
				resp.Body.Close()
			}
		}()
		time.Sleep(500 * time.Millisecond)
		took := s.stop()
		if took > 8*time.Second || s.err != nil && strings.Contains(s.err.Error(), "killed") {
			t.Fatalf("SIGTERM with a frozen database: exit %v after %v\n%s", s.err, took, s.logs.String())
		}
		t.Logf("measured: exit %v after %v with a request waiting for a frozen database", s.err, took.Round(time.Millisecond))
	})
}

// Row 9 (configuration): settings that stop the start, and a short token
// that is only a warning.
func TestModuleModeConfig(t *testing.T) {
	m := newModuleEnv(t)
	m.migrate()
	const leak = "u:leakcheck-password@tcp(127.0.0.1:1)/x"
	type tc struct {
		override env
		args     []string
		says     string
	}
	serve := []string{"serve", "--config", m.config, "--socket", m.sock}
	noSocket := []string{"serve", "--config", m.config}
	exp := filepath.Join(t.TempDir(), "expansion.env")
	writeFile(t, exp, "MFGRAPH_MODE=module\nMFGRAPH_DSN=${DSN}\n")
	withParam := func(dsn, param string) string {
		if strings.Contains(dsn, "?") {
			return dsn + "&" + param
		}
		return dsn + "?" + param
	}
	opsDSN, dsn := m.env["MFGRAPH_OPS_DSN"], m.env["MFGRAPH_DSN"]
	cases := map[string]tc{
		"TCP address":        {env{"MODULE_SOCKET": "", "MFGRAPH_HTTP_ADDR": "127.0.0.1:0"}, noSocket, "Unix domain socket"},
		"no socket":          {env{"MODULE_SOCKET": ""}, noSocket, "socket"},
		"no token":           {env{"MODULE_INTERNAL_TOKEN": ""}, serve, "MODULE_INTERNAL_TOKEN"},
		"no DSN":             {env{"MFGRAPH_DSN": ""}, serve, "MFGRAPH_DSN"},
		"no data directory":  {env{"MFGRAPH_DATA_DIR": ""}, serve, "MFGRAPH_DATA_DIR"},
		"no tenant":          {env{"MFGRAPH_TENANT_ID": ""}, serve, "MFGRAPH_TENANT_ID"},
		"all-zero tenant":    {env{"MFGRAPH_TENANT_ID": "00000000000000000000000000"}, serve, "all-zero"},
		"wrong MODULE_ID":    {env{"MODULE_ID": "files"}, serve, "MODULE_ID"},
		"aliases disagree":   {env{"MFGRAPH_DB_DSN": leak}, serve, "MFGRAPH_DB_DSN and MFGRAPH_DSN"},
		"kernel migrations":  {env{"MFGRAPH_KERNEL_MIGRATIONS": "true"}, serve, "MFGRAPH_KERNEL_MIGRATIONS"},
		"expansion in file":  {nil, []string{"serve", "--config", exp, "--socket", m.sock}, "line 2"},
		"socket of the host": {env{"MFGRAPH_HTTP_ADDR": "unix:/elsewhere.sock"}, serve, "MODULE_SOCKET and MFGRAPH_HTTP_ADDR"},
		// No database name and '/' in the password: the driver's message
		// would quote "zzops:ZzOps".
		"malformed ops DSN": {env{"MFGRAPH_OPS_DSN": "zzops:ZzOps/Pw-77gg@tcp(127.0.0.1:33406)"}, serve,
			"MFGRAPH_OPS_DSN is not a valid DSN"},
		"malformed DSN": {env{"MFGRAPH_DSN": "zzops:ZzOps/Pw-77gg@tcp(127.0.0.1:33406)"}, []string{"--config", m.config, "migrate"},
			"MFGRAPH_DSN is not a valid DSN"},
		// The forwarder's connection is utf8mb4 or nothing.
		"ops charset utf8":   {env{"MFGRAPH_OPS_DSN": withParam(opsDSN, "charset=utf8")}, serve, "MFGRAPH_OPS_DSN sets the character set utf8;"},
		"ops charset latin1": {env{"MFGRAPH_OPS_DSN": withParam(opsDSN, "charset=latin1")}, serve, "character set latin1"},
		"ops charset list":   {env{"MFGRAPH_OPS_DSN": withParam(opsDSN, "charset=utf8,utf8mb4")}, serve, "list of character sets"},
		"DSN charset without an ops DSN": {env{"MFGRAPH_OPS_DSN": "", "MFGRAPH_DSN": withParam(dsn, "charset=utf8")}, serve,
			"MFGRAPH_DSN sets the character set utf8;"},
	}
	for name, c := range cases {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		out, err := m.commandContext(ctx, c.override, c.args...).CombinedOutput()
		cancel()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 2 {
			t.Errorf("%s: %v, want exit code 2\n%s", name, err, out)
			continue
		}
		if !strings.Contains(string(out), c.says) || strings.Contains(string(out), "listening") {
			t.Errorf("%s: the message does not name %q:\n%s", name, c.says, out)
		}
		if strings.Contains(string(out), modToken) || strings.Contains(string(out), "leakcheck-password") ||
			strings.Contains(string(out), "ZzOps") || strings.Contains(string(out), "Pw-77gg") {
			t.Errorf("%s: the output leaks a secret:\n%s", name, out)
		}
	}
	// A short token: a warning, and the server starts.
	s := m.start(env{"MODULE_INTERNAL_TOKEN": "dev-token"})
	if !strings.Contains(s.logs.String(), "MODULE_INTERNAL_TOKEN has fewer than 16 characters") || strings.Contains(s.logs.String(), "dev-token") {
		t.Fatalf("short token warning:\n%s", s.logs.String())
	}
	short := defaultClaims(projP)
	short.token = "dev-token"
	if r := s.do(short, "GET", modBase+"/workspaces/01JD3X5Q8Z9W2V4T6R8P0N2MZZ", nil, nil); r.status != 404 {
		t.Fatalf("a request with the short token: %d", r.status)
	}
}
