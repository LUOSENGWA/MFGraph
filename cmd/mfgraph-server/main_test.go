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

// TestServeHealthAnd501 starts the real server wiring (no database) and
// checks health, the 501 envelope and graceful shutdown.
func TestServeHealthAnd501(t *testing.T) {
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
	if code, body := get("/readyz", false); code != 200 || body["checks"].(map[string]any)["db"] != "not_configured" {
		t.Fatalf("readyz: %d %v", code, body)
	}
	if code, body := get("/api/v1/snapshots/"+testUID, true); code != 501 || body["code"] != "not_implemented" {
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
	db, err := openDB(context.Background(), dsn)
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
