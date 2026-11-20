//go:build e2e

package e2e

import (
	"archive/tar"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/api/gen"
)

// upgradeBase is the commit of main whose schema precedes migration 0009
// (M1.0a, before the module form); MFGRAPH_E2E_UPGRADE_FROM names another.
const upgradeBase = "dd5359e04530e3516ff638a417fe94c6cae751cd"

// oldServer builds the server of upgradeBase from the git history (git
// archive, then go build without network access). Without git or the
// commit (a shallow clone) the test is skipped, unless
// MFGRAPH_E2E_REQUIRE_UPGRADE=1 (CI) makes that a failure.
func oldServer(t *testing.T) string {
	t.Helper()
	commit := os.Getenv("MFGRAPH_E2E_UPGRADE_FROM")
	if commit == "" {
		commit = upgradeBase
	}
	unavailable := func(why string, err error) string {
		t.Helper()
		if os.Getenv("MFGRAPH_E2E_REQUIRE_UPGRADE") == "1" {
			t.Fatalf("the server of %s cannot be built (%s): %v", commit, why, err)
		}
		t.Skipf("the server of %s cannot be built (%s: %v); fetch the full history to run this test", commit, why, err)
		return ""
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	archive, err := exec.Command("git", "-C", root, "archive", "--format=tar", commit).Output()
	if err != nil {
		return unavailable("git archive", err)
	}
	dir := t.TempDir()
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(dir, filepath.FromSlash(h.Name))
		if !strings.HasPrefix(target, dir+string(filepath.Separator)) {
			t.Fatalf("archive entry %q", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			if err = os.MkdirAll(filepath.Dir(target), 0o755); err == nil {
				var b []byte
				if b, err = io.ReadAll(tr); err == nil {
					err = os.WriteFile(target, b, 0o644)
				}
			}
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(dir, "mfgraph-server-"+commit[:7])
	build := exec.Command("go", "build", "-o", bin, "./cmd/mfgraph-server")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOFLAGS=-mod=readonly", "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		return unavailable("go build: "+strings.TrimSpace(string(out)), err)
	}
	return bin
}

// schemaOf describes the tables of a database: every column (position,
// type, nullability, default, collation) and every index.
func schemaOf(t *testing.T, dsn string) string {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var out []string
	for _, q := range []string{
		`SELECT table_name, ordinal_position, column_name, column_type, is_nullable, IFNULL(column_default, 'NULL'),
			IFNULL(collation_name, '') FROM information_schema.columns WHERE table_schema = DATABASE()
			ORDER BY table_name, ordinal_position`,
		`SELECT table_name, index_name, non_unique, seq_in_index, column_name FROM information_schema.statistics
			WHERE table_schema = DATABASE() ORDER BY table_name, index_name, seq_in_index`,
	} {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]sql.NullString, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			var line []string
			for _, v := range vals {
				line = append(line, v.String)
			}
			out = append(out, strings.Join(line, " "))
		}
		rows.Close()
	}
	return strings.Join(out, "\n")
}

// TestUpgradeFromMain: the schema and data of the last release before 0009
// (built from main at upgradeBase) are upgraded by this build:
//
//  1. the old server creates its schema and data (project, workspace,
//     uploads, commits, an idempotent commit, a restore);
//  2. this server on that schema without migrating: /readyz 503
//     schema_behind with mfgraph/0009_actor_types, and an ERROR line;
//  3. migrate applies the outstanding migrations;
//  4. this server serves the old data and replays the old idempotent
//     commit; the schema equals that of a fresh installation;
//  5. rollback: the old server runs on the new schema without automatic
//     migration, and refuses to start with it.
//
// It runs on MySQL 8.0 and seekdb like every e2e test.
func TestUpgradeFromMain(t *testing.T) {
	old := oldServer(t)
	e := newEnv(t) // MFGRAPH_MIGRATE_ON_START=true
	s := startBinary(t, old, e)
	c := s.client(tokenA)
	sd := seed(t, c)
	h := c.upload(sd.ws.WorkspaceId, "idempotent before the upgrade\n")
	req := gen.CommitRequest{BranchId: sd.branch.BranchId, ExpectedGeneration: sd.first.Generation, Changes: []gen.Change{put("c.txt", h)}}
	var idem gen.CommitResult
	c.expect(c.commitRaw(sd.ws.WorkspaceId, req, "upgrade-key-1"), "old server: idempotent commit", http.StatusCreated, &idem)
	var restored gen.CommitResult
	c.expect(c.json("POST", "/api/v1/snapshots/"+sd.first.SnapshotId+"/restore",
		gen.RestoreRequest{BranchId: sd.branch.BranchId, ExpectedGeneration: idem.Generation}, nil), "old server: restore", http.StatusCreated,
		&restored)
	s.stop()

	// 2. Not migrated: not ready, and an ERROR line.
	noMigrate := env{}
	for k, v := range e {
		noMigrate[k] = v
	}
	noMigrate["MFGRAPH_MIGRATE_ON_START"] = "false"
	n := start(t, noMigrate)
	r := n.client(tokenA).do("GET", "/readyz", nil, nil)
	var envl struct {
		Code    string         `json:"code"`
		Details map[string]any `json:"details"`
	}
	_ = json.Unmarshal(r.body, &envl)
	if r.status != 503 || envl.Code != "db_unavailable" || envl.Details["check"] != "db" || envl.Details["reason"] != "schema_behind" ||
		fmt.Sprint(envl.Details["missing"]) != "[mfgraph/0009_actor_types mfgraph/0010_outbox_order mfgraph/0011_packed_blobs]" {
		t.Fatalf("readyz before migrate: %d %s", r.status, r.body)
	}
	if !strings.Contains(n.logs.String(), "level=ERROR") || !strings.Contains(n.logs.String(), "schema is behind this build") {
		t.Fatalf("no ERROR line for the schema:\n%s", n.logs.String())
	}
	n.stop()

	// 3. migrate: the outstanding migrations.
	cmd := exec.Command(serverBin, "migrate")
	cmd.Env = append(os.Environ(), "MFGRAPH_DB_DSN="+e["MFGRAPH_DB_DSN"])
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"component":"mfgraph","applied":["0009_actor_types","0010_outbox_order","0011_packed_blobs"],"skipped":6`) {
		t.Fatalf("migrate: %v\n%s", err, out)
	}

	// 4. The old data with this server.
	n = start(t, noMigrate)
	c = n.client(tokenA)
	if r := c.do("GET", "/readyz", nil, nil); r.status != 200 {
		t.Fatalf("readyz after migrate: %d %s", r.status, r.body)
	}
	snap := c.snapshot(sd.first.SnapshotId)
	if snap.AuthorRef != actorA || snap.AuthorType != nil {
		t.Fatalf("old snapshot: %+v", snap)
	}
	if got := c.file(restored.SnapshotId, "a.txt", nil); got.status != 200 || string(got.body) != contentA {
		t.Fatalf("file of the old restore: %d %q", got.status, got.body)
	}
	if l := c.tree(idem.SnapshotId, ""); len(l.Items) != 3 {
		t.Fatalf("tree of the old commit: %+v", l.Items)
	}
	var replay gen.CommitResult
	c.expect(c.commitRaw(sd.ws.WorkspaceId, req, "upgrade-key-1"), "replay of the old idempotent commit", http.StatusCreated, &replay)
	if replay.SnapshotId != idem.SnapshotId {
		t.Fatalf("replay: %s, want %s", replay.SnapshotId, idem.SnapshotId)
	}
	next := c.commit(sd.ws.WorkspaceId, sd.branch.BranchId, restored.Generation, put("after.txt", c.upload(sd.ws.WorkspaceId, "after\n")))
	if c.snapshot(next.SnapshotId).AuthorRef != actorA {
		t.Fatal("new commit after the upgrade")
	}
	n.stop()
	fresh := newEnv(t)
	cmd = exec.Command(serverBin, "migrate")
	cmd.Env = append(os.Environ(), "MFGRAPH_DB_DSN="+fresh["MFGRAPH_DB_DSN"])
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fresh migrate: %v\n%s", err, out)
	}
	if up, want := schemaOf(t, e["MFGRAPH_DB_DSN"]), schemaOf(t, fresh["MFGRAPH_DB_DSN"]); up != want {
		t.Fatalf("upgraded schema differs from a fresh installation:\n%s\n---\n%s", up, want)
	}

	// 5. Rollback: the old server on the new schema.
	o := startBinary(t, old, noMigrate)
	if r := o.client(tokenA).do("GET", "/readyz", nil, nil); r.status != 200 {
		t.Fatalf("old server on the new schema: readyz %d %s", r.status, r.body)
	}
	if s := o.client(tokenA).snapshot(next.SnapshotId); s.AuthorRef != actorA {
		t.Fatalf("old server reads a new snapshot: %+v", s)
	}
	o.stop()
	cmd = exec.Command(old)
	cmd.Env = append(os.Environ(), "MFGRAPH_HTTP_ADDR="+freeAddr(t))
	for k, v := range e { // MFGRAPH_MIGRATE_ON_START=true
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err = cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || !strings.Contains(string(out), "applied migration missing from source") {
		t.Fatalf("old server with MFGRAPH_MIGRATE_ON_START=true on the new schema: %v\n%s", err, out)
	}
	t.Logf("measured: upgraded from %s with data; schema = fresh installation; rollback works only without automatic migration",
		upgradeBase[:7])
}
