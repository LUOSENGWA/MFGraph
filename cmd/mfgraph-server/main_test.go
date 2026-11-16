package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/config"
)

const (
	testToken = "server-test-token-0123456789"
	testUID   = "01JD3X5Q8Z9W2V4T6R8P0N2M4K"
)

func TestVersionAndUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"version"}, func(string) string { return "" }, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "mfgraph-server ") {
		t.Fatalf("version: %d %q", code, out.String())
	}
	if code := run(context.Background(), []string{"bogus"}, func(string) string { return "" }, &out, &errOut); code != 2 {
		t.Fatalf("unknown command exit %d", code)
	}
	env := map[string]string{config.EnvMigrateOnStart: "maybe"}
	if code := run(context.Background(), nil, func(k string) string { return env[k] }, &out, &errOut); code != 2 {
		t.Fatalf("bad config exit %d", code)
	}
	if code := run(context.Background(), []string{"migrate"}, func(string) string { return "" }, &out, &errOut); code != 1 {
		t.Fatalf("migrate without DSN exit %d", code)
	}
}

// TestServeWithoutDatabase starts the real server wiring without a
// database and checks health, readiness (not ready), the 503 envelope of the
// business endpoints and graceful shutdown.
func TestServeWithoutDatabase(t *testing.T) {
	cfg, err := config.Load(func(k string) string {
		return map[string]string{config.EnvAuthTokens: testToken + "=" + testUID}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, ln, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	base := "http://" + ln.Addr().String()

	get := func(path string, authz bool) (int, map[string]any) {
		req, _ := http.NewRequest("GET", base+path, nil)
		if authz {
			req.Header.Set("Authorization", "Bearer "+testToken)
		}
		var resp *http.Response
		for i := 0; i < 50; i++ {
			if resp, err = http.DefaultClient.Do(req); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}
	if code, body := get("/healthz", false); code != 200 || body["status"] != "ok" {
		t.Fatalf("healthz: %d %v", code, body)
	}
	if code, body := get("/readyz", false); code != 503 || body["code"] != "db_unavailable" {
		t.Fatalf("readyz: %d %v", code, body)
	}
	if code, body := get("/api/v1/snapshots/"+testUID, true); code != 503 || body["code"] != "db_unavailable" {
		t.Fatalf("snapshot: %d %v", code, body)
	}
	if code, body := get("/api/v1/snapshots/"+testUID, false); code != 401 || body["code"] != "unauthenticated" {
		t.Fatalf("unauthenticated: %d %v", code, body)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
}

// TestMigrateCommandMySQL applies the migrations through the migrate
// subcommand when a test database is available.
func TestMigrateCommandMySQL(t *testing.T) {
	dsn := os.Getenv("MFGRAPH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MFGRAPH_TEST_MYSQL_DSN not set")
	}
	// With MFGRAPH_TEST_DB_WAIT (a duration), wait that long for the server
	// (seekdb initializes a fresh data directory on first start).
	var wait time.Duration
	if v := os.Getenv("MFGRAPH_TEST_DB_WAIT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatalf("MFGRAPH_TEST_DB_WAIT: %v", err)
		}
		wait = d
	}
	db, err := openDB(context.Background(), dsn)
	for deadline := time.Now().Add(wait); err != nil && time.Now().Before(deadline); {
		time.Sleep(2 * time.Second)
		db, err = openDB(context.Background(), dsn)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	name := "mfgraph_cmdtest_" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	if _, err := db.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("DROP DATABASE " + name)
	env := map[string]string{config.EnvDBDSN: strings.TrimSuffix(dsn, "/") + "/" + name}
	var out, errOut bytes.Buffer
	for i := 0; i < 2; i++ { // second run skips everything
		if code := run(context.Background(), []string{"migrate"}, func(k string) string { return env[k] }, &out, &errOut); code != 0 {
			t.Fatalf("migrate run %d: exit %d: %s", i+1, code, errOut.String())
		}
	}
	if !strings.Contains(errOut.String(), `"skipped":6`) {
		t.Fatalf("second run did not skip: %s", errOut.String())
	}
}

// TestRefusesMismatchedUnicodeTables: serve and migrate stop before touching
// anything when the Unicode tables of the build do not match V, and the log
// names the actual and the required versions. version and help still work.
func TestRefusesMismatchedUnicodeTables(t *testing.T) {
	defer func(f func() (string, string, string)) { unicodeTables = f }(unicodeTables)
	for _, tables := range [][3]string{
		{"17.0.0", "17.0.0", "17.0.0"}, // a newer Go without raising V
		{"15.0.0", "14.0.0", "15.0.0"}, // x/text older than V
	} {
		unicodeTables = func() (string, string, string) { return tables[0], tables[1], tables[2] }
		for _, cmd := range []string{"serve", "migrate"} {
			var out, errOut bytes.Buffer
			env := map[string]string{config.EnvHTTPAddr: "127.0.0.1:0", config.EnvDBDSN: "u:p@tcp(127.0.0.1:1)/x"}
			code := run(context.Background(), []string{cmd}, func(k string) string { return env[k] }, &out, &errOut)
			if code == 0 {
				t.Fatalf("%s with tables %v: exit 0", cmd, tables)
			}
			log := errOut.String()
			for _, want := range []string{"refusing to start", `"required_v":"15.0.0"`, `"unicode":"` + tables[0] + `"`,
				`"x_text_norm":"` + tables[1] + `"`} {
				if !strings.Contains(log, want) {
					t.Errorf("%s with tables %v: log lacks %s:\n%s", cmd, tables, want, log)
				}
			}
			if strings.Contains(log, "listening") || strings.Contains(log, "migrations") || strings.Contains(log, "connect to database") {
				t.Errorf("%s with tables %v went on after the check:\n%s", cmd, tables, log)
			}
		}
		var out, errOut bytes.Buffer
		if code := run(context.Background(), []string{"version"}, func(string) string { return "" }, &out, &errOut); code != 0 {
			t.Errorf("version must not depend on the Unicode tables: exit %d", code)
		}
	}
}

// TestServeOnUnixSocket: MFGRAPH_HTTP_ADDR=unix:<path> serves on a socket of
// mode 0600; a stale socket is replaced; a regular file, or the socket of a
// running server, stops the start.
func TestServeOnUnixSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "mfg") // short: socket paths are limited to about 100 bytes
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "api.sock")
	// A stale socket: bound, then left behind without a server.
	stale, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()
	cfg, err := config.Load(func(k string) string {
		return map[string]string{config.EnvHTTPAddr: "unix:" + sock, config.EnvAuthTokens: testToken + "=" + testUID}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := listen(cfg)
	if err != nil {
		t.Fatalf("listen over a stale socket: %v", err)
	}
	fi, err := os.Stat(sock)
	if err != nil || fi.Mode().Perm() != 0o600 || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket mode: %v %v", fi.Mode(), err)
	}
	if _, err := listen(cfg); err == nil || !strings.Contains(err.Error(), "running server") {
		t.Fatalf("second server on a live socket: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, ln, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	resp, err := client.Get("http://mfgraph/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("healthz over the socket: %d", resp.StatusCode)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket not removed on shutdown: %v", err)
	}
	// A regular file at the path is never replaced.
	if err := os.WriteFile(sock, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := listen(cfg); err == nil || !strings.Contains(err.Error(), "not a socket") {
		t.Fatalf("listen over a regular file: %v", err)
	}
	if b, _ := os.ReadFile(sock); string(b) != "data" {
		t.Fatal("the regular file was changed")
	}
}

// TestShutdownTimeoutDefault: graceful shutdown is bounded by 8 s unless
// configured otherwise.
func TestShutdownTimeoutDefault(t *testing.T) {
	cfg, err := config.Load(func(string) string { return "" })
	if err != nil || cfg.ShutdownTimeout != 8*time.Second || cfg.HTTPAddr != "127.0.0.1:8094" {
		t.Fatalf("%+v %v", cfg, err)
	}
}
