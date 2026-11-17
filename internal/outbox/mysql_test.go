package outbox_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/LUOSENGWA/MFGraph/internal/authz"
	"github.com/LUOSENGWA/MFGraph/internal/blob/posix"
	"github.com/LUOSENGWA/MFGraph/internal/core"
	"github.com/LUOSENGWA/MFGraph/internal/ids"
	"github.com/LUOSENGWA/MFGraph/internal/migrate"
	"github.com/LUOSENGWA/MFGraph/internal/outbox"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	store "github.com/LUOSENGWA/MFGraph/internal/store/mysql"
	"github.com/LUOSENGWA/MFGraph/internal/treestore"
	"github.com/LUOSENGWA/MFGraph/migrations"
)

// HostOutboxDDL is the host platform's outbox table as its migration
// creates it (actor_id widened to 255 characters); the tests create it as
// a fixture, it is never part of MFGraph's migrations.
const HostOutboxDDL = `CREATE TABLE IF NOT EXISTS module_outbox (
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

const (
	tenantM  = "01JD3X5Q8Z9W2V4T6R8P0N2T00"
	projectM = "01JD3X5Q8Z9W2V4T6R8P0N2P01"
)

// mysqlEnv is a module deployment on the test server: the MFGraph schema,
// the host's ops schema with its outbox table, a service in module mode
// and a forwarder.
type mysqlEnv struct {
	t        *testing.T
	db, ops  *sql.DB
	store    *store.Store
	svc      *core.Service
	fwd      *outbox.Forwarder
	sink     *outbox.SQLSink
	opsName  string
	failDone bool // makes CompleteIdempotency fail after the events were appended
}

type failingStore struct {
	*store.Store
	e *mysqlEnv
}

func (f failingStore) CompleteIdempotency(ctx context.Context, tx ports.Tx, actor, route, key string, at int64, resp []byte) error {
	if f.e.failDone {
		return errors.New("injected failure after the events")
	}
	return f.Store.CompleteIdempotency(ctx, tx, actor, route, key, at, resp)
}

func newMySQLEnv(t *testing.T) *mysqlEnv {
	t.Helper()
	dsn := os.Getenv("MFGRAPH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MFGRAPH_TEST_MYSQL_DSN not set (run `make test-mysql`)")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	var wait time.Duration
	if v := os.Getenv("MFGRAPH_TEST_DB_WAIT"); v != "" {
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
		t.Fatal(err)
	}
	name := fmt.Sprintf("mfgraph_obtest_%d", time.Now().UnixNano())
	e := &mysqlEnv{t: t, opsName: name + "_ops"}
	for _, db := range []string{name, e.opsName} {
		if _, err := admin.Exec("CREATE DATABASE " + db + " CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
			t.Fatal(err)
		}
		db := db
		t.Cleanup(func() { _, _ = admin.Exec("DROP DATABASE IF EXISTS " + db) })
	}
	open := func(db string) *sql.DB {
		c := cfg.Clone()
		c.DBName = db
		d, err := sql.Open("mysql", c.FormatDSN())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		return d
	}
	e.db, e.ops = open(name), open(e.opsName)
	if _, err := e.ops.Exec(HostOutboxDDL); err != nil {
		t.Fatalf("host outbox table: %v", err)
	}
	if _, err := migrate.RunAll(ctx, migrate.SQLTarget{DB: e.db}, []migrate.Plan{{Component: migrate.ComponentMFGraph,
		Source: migrations.MySQL()}}, time.Now); err != nil {
		t.Fatal(err)
	}
	e.store = store.New(e.db)
	objs, err := posix.New(t.TempDir(), quiet())
	if err != nil {
		t.Fatal(err)
	}
	trees, err := treestore.New(objs, func(ctx context.Context, ws string) (string, error) {
		w, err := e.store.GetWorkspace(ctx, nil, ws)
		return w.StoragePrefix, err
	})
	if err != nil {
		t.Fatal(err)
	}
	e.svc, err = core.New(core.Config{TenantID: tenantM, HostOwnsProjects: true}, core.Deps{Store: failingStore{e.store, e},
		Objects: objs, Trees: trees, Authz: authz.ProjectScope{}, Clock: ports.ClockFunc(time.Now), IDs: ids.NewGenerator(),
		Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	if e.sink, err = outbox.NewSQLSink(e.ops, e.opsName+".module_outbox"); err != nil {
		t.Fatal(err)
	}
	e.fwd = outbox.New(e.store, e.sink, outbox.Options{Logger: quiet()})
	return e
}

type hostRow struct {
	id                                    int64
	eventID, module, kind, scope, scopeID string
	actorType, actorID                    sql.NullString
	payload, status                       string
	attempts                              int
	availableAt, createdAt, updatedAt     string
}

func (e *mysqlEnv) hostRows() []hostRow {
	e.t.Helper()
	rows, err := e.ops.Query(`SELECT id, event_id, module, event_kind, scope_kind, scope_id, actor_type, actor_id, payload_json,
		status, attempts, CAST(available_at AS CHAR), CAST(created_at AS CHAR), CAST(updated_at AS CHAR) FROM module_outbox ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []hostRow
	for rows.Next() {
		var r hostRow
		if err := rows.Scan(&r.id, &r.eventID, &r.module, &r.kind, &r.scope, &r.scopeID, &r.actorType, &r.actorID, &r.payload,
			&r.status, &r.attempts, &r.availableAt, &r.createdAt, &r.updatedAt); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (e *mysqlEnv) flush() int {
	e.t.Helper()
	n, err := e.fwd.Flush(ctx)
	if err != nil {
		e.t.Fatalf("flush: %v", err)
	}
	return n
}

func kindsOf(rows []hostRow) string {
	var out []string
	for _, r := range rows {
		out = append(out, strings.TrimPrefix(r.kind, "mfgraph."))
	}
	return strings.Join(out, ",")
}

// TestMySQLForwarder runs the forwarder against the real servers (MySQL
// 8.0 and seekdb): the rows of the host table, rollback, deduplication,
// a host table that is gone for a while, the order, and a 255-byte actor.
func TestMySQLForwarder(t *testing.T) {
	e := newMySQLEnv(t)
	actor := "worker-" + strings.Repeat("w", 255-len("worker-")) // 255 bytes
	p := ports.Principal{Ref: actor, ActorType: "worker", ProjectScope: projectM, OnBehalfOfRef: "manager", OnBehalfOfType: "human"}
	if err := e.sink.Check(ctx); err != nil {
		t.Fatalf("readiness check: %v", err)
	}
	ws, err := e.svc.CreateWorkspace(ctx, p, core.CreateWorkspaceRequest{ProjectID: projectM}, "")
	if err != nil {
		t.Fatal(err)
	}
	blob, _, err := e.svc.PutObject(ctx, p, core.PutObjectRequest{WorkspaceID: ws.Workspace.ID, Body: strings.NewReader("hello"),
		ContentLength: -1})
	if err != nil {
		t.Fatal(err)
	}
	h := blob.Hash.String()
	c1, err := e.svc.Commit(ctx, p, ws.Workspace.ID, core.CommitRequest{BranchID: ws.MainBranchID, ExpectedGeneration: 1,
		Changes: []core.Change{{Path: "f.txt", Hash: &h}}}, "k-1")
	if err != nil {
		t.Fatal(err)
	}
	// Rolled back after its events were appended: nothing, here or there.
	e.failDone = true
	del := true
	if _, err := e.svc.Commit(ctx, p, ws.Workspace.ID, core.CommitRequest{BranchID: ws.MainBranchID, ExpectedGeneration: 2,
		Changes: []core.Change{{Path: "f.txt", Delete: &del}}}, "k-2"); err == nil {
		t.Fatal("the injected failure did not fail the commit")
	}
	e.failDone = false
	if evs, _ := e.store.Events(ctx); len(evs) != 4 {
		t.Fatalf("local outbox after a rollback: %d events, want 4", len(evs))
	}
	if n := e.flush(); n != 4 {
		t.Fatalf("delivered %d, want 4", n)
	}
	rows := e.hostRows()
	if got := kindsOf(rows); got != "workspace_created,branch_created,commit_created,branch_updated" {
		t.Fatalf("host rows: %s", got)
	}
	local, _ := e.store.Events(ctx)
	byID := map[string]ports.Event{}
	for _, ev := range local {
		var env struct {
			EventID string `json:"event_id"`
		}
		_ = json.Unmarshal(ev.Payload, &env)
		byID[env.EventID] = ev
	}
	keys := []string{ws.Workspace.ID, ws.MainBranchID, c1.SnapshotID, ws.MainBranchID}
	versions := []int64{1, 1, 1, 2}
	for i, r := range rows {
		sum := sha256.Sum256([]byte(fmt.Sprintf("mfgraph\n%s\n%s\n%s\n%d", r.kind, projectM, keys[i], versions[i])))
		ev, ok := byID[r.eventID]
		var payload, stored map[string]any
		_ = json.Unmarshal([]byte(r.payload), &stored)
		_ = json.Unmarshal(ev.Payload, &payload)
		if !ok || r.eventID != hex.EncodeToString(sum[:]) || r.module != "mfgraph" || r.scope != "project" || r.scopeID != projectM ||
			r.actorType.String != "worker" || r.actorID.String != actor || r.status != "pending" || r.attempts != 0 ||
			fmt.Sprint(stored) != fmt.Sprint(payload) {
			t.Errorf("host row %d: %+v", i, r)
		}
		occurred := payload["occurred_at"].(string)
		created, err := outbox.Time(r.createdAt)
		want, _ := time.Parse(time.RFC3339Nano, occurred)
		if err != nil || !created.Equal(want) {
			t.Errorf("host row %d: created_at %s, occurred_at %s", i, r.createdAt, occurred)
		}
		available, err := outbox.Time(r.availableAt)
		if err != nil || time.Since(available) > time.Minute || time.Since(available) < -time.Minute || r.updatedAt != r.availableAt {
			t.Errorf("host row %d: available_at %s, updated_at %s (UTC now %s)", i, r.availableAt, r.updatedAt, time.Now().UTC())
		}
	}
	if d, _ := e.store.EventDelivery(ctx, local[0].ID); d.Status != ports.EventPublished || d.PublishedAtUs == 0 {
		t.Fatalf("local row after delivery: %+v", d)
	}

	// Delivered again (as after a crash before the mark): still one row each.
	if _, err := e.db.Exec("UPDATE outbox_events SET status = 'pending', published_at_us = NULL"); err != nil {
		t.Fatal(err)
	}
	if n := e.flush(); n != 4 {
		t.Fatalf("redelivered %d", n)
	}
	if again := e.hostRows(); len(again) != 4 {
		t.Fatalf("redelivery added rows: %d", len(again))
	}
	// The same commit replayed: no new events.
	if r, err := e.svc.Commit(ctx, p, ws.Workspace.ID, core.CommitRequest{BranchID: ws.MainBranchID, ExpectedGeneration: 1,
		Changes: []core.Change{{Path: "f.txt", Hash: &h}}}, "k-1"); err != nil || r.SnapshotID != c1.SnapshotID {
		t.Fatalf("replay: %+v %v", r, err)
	}
	if n := e.flush(); n != 0 {
		t.Fatalf("a replay delivered %d events", n)
	}

	// The host table is gone for a while: the events wait, in order.
	if _, err := e.ops.Exec("RENAME TABLE module_outbox TO module_outbox_away"); err != nil {
		t.Fatal(err)
	}
	if err := e.sink.Check(ctx); err == nil {
		t.Fatal("readiness with the host table gone")
	}
	r1, err := e.svc.Restore(ctx, p, c1.SnapshotID, core.RestoreRequest{BranchID: ws.MainBranchID, ExpectedGeneration: 2,
		Path: strPtr("missing.txt")}, "")
	if core.CodeOf(err) != core.CodeInvalidRequest {
		t.Fatalf("restore of a path that exists nowhere: %+v %v", r1, err)
	}
	c2, err := e.svc.Commit(ctx, p, ws.Workspace.ID, core.CommitRequest{BranchID: ws.MainBranchID, ExpectedGeneration: 2,
		Changes: []core.Change{{Path: "f.txt", Delete: &del}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := e.svc.Restore(ctx, p, c1.SnapshotID, core.RestoreRequest{BranchID: ws.MainBranchID, ExpectedGeneration: 3}, "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if n, err := e.fwd.Flush(ctx); n != 0 || err == nil {
			t.Fatalf("flush without the host table: %d %v", n, err)
		}
	}
	pending, _ := e.store.PendingEvents(ctx, 10)
	if len(pending) != 4 {
		t.Fatalf("pending: %d", len(pending))
	}
	if d, _ := e.store.EventDelivery(ctx, pending[0].ID); d.RetryCount != 2 || d.Status != ports.EventPending {
		t.Fatalf("first pending event: %+v", d)
	}
	if d, _ := e.store.EventDelivery(ctx, pending[1].ID); d.RetryCount != 0 {
		t.Fatalf("an event after the failed one was tried: %+v", d)
	}
	if _, err := e.ops.Exec("RENAME TABLE module_outbox_away TO module_outbox"); err != nil {
		t.Fatal(err)
	}
	if n := e.flush(); n != 4 {
		t.Fatalf("after the table is back: %d", n)
	}
	rows = e.hostRows()
	if got := kindsOf(rows); got != "workspace_created,branch_created,commit_created,branch_updated,commit_created,branch_updated,"+
		"snapshot_restored,branch_updated" {
		t.Fatalf("host rows: %s", got)
	}
	var tail []string
	for _, r := range rows[4:] {
		var env struct {
			Data map[string]any `json:"data"`
		}
		_ = json.Unmarshal([]byte(r.payload), &env)
		tail = append(tail, fmt.Sprintf("%v %v %v", env.Data["snapshot_id"], env.Data["head_snapshot_id"], env.Data["reason"]))
	}
	want := []string{c2.SnapshotID + " <nil> <nil>", "<nil> " + c2.SnapshotID + " commit", r2.SnapshotID + " <nil> <nil>",
		"<nil> " + r2.SnapshotID + " restore"}
	if strings.Join(tail, "|") != strings.Join(want, "|") {
		t.Fatalf("order of the late events:\n%v\nwant\n%v", tail, want)
	}
	t.Logf("measured: %d host rows in outbox order; a duplicate event_id is absorbed by %s", len(rows), outbox.UniqueKey)
}

// TestMySQLInsertOnlyAccount: the forwarder's account may have INSERT on
// the host table and nothing else: rows are written, a duplicate counts as
// delivered, and the readiness check finds the table.
func TestMySQLInsertOnlyAccount(t *testing.T) {
	e := newMySQLEnv(t)
	user := fmt.Sprintf("mfg_ops_%d", time.Now().UnixNano()%1_000_000_000)
	const password = "ops-test-0123456789"
	for _, stmt := range []string{
		"CREATE USER '" + user + "'@'%' IDENTIFIED BY '" + password + "'",
		"GRANT INSERT ON " + e.opsName + ".module_outbox TO '" + user + "'@'%'",
	} {
		if _, err := e.ops.Exec(stmt); err != nil {
			t.Skipf("cannot create a test account on this server (%v)", err)
		}
	}
	t.Cleanup(func() { _, _ = e.ops.Exec("DROP USER '" + user + "'@'%'") })
	cfg, _ := mysql.ParseDSN(os.Getenv("MFGRAPH_TEST_MYSQL_DSN"))
	cfg.User, cfg.Passwd, cfg.DBName = user, password, ""
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sink, err := outbox.NewSQLSink(db, e.opsName+".module_outbox")
	if err != nil {
		t.Fatal(err)
	}
	var checkErr error
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(time.Second) { // grants may take a moment on seekdb
		if checkErr = sink.Check(ctx); checkErr == nil || time.Now().After(deadline) {
			break
		}
	}
	if checkErr != nil {
		t.Fatalf("readiness with INSERT only: %v", checkErr)
	}
	if _, err := db.Exec("SELECT 1 FROM " + e.opsName + ".module_outbox LIMIT 0"); err == nil {
		t.Log("note: this server allows the SELECT without the privilege")
	}
	row := outbox.Row{EventID: strings.Repeat("c", 64), EventKind: ports.EventWorkspaceCreated, ScopeID: projectM, ActorID: "manager",
		Payload: []byte(`{"v":1}`), CreatedAt: time.Now(), AvailableAt: time.Now(), UpdatedAt: time.Now()}
	for i := 0; i < 2; i++ {
		if err := sink.Insert(ctx, row); err != nil {
			t.Fatalf("insert %d with INSERT only: %v", i+1, err)
		}
	}
	if n := len(e.hostRows()); n != 1 {
		t.Fatalf("%d rows", n)
	}
	other, _ := outbox.NewSQLSink(db, e.opsName+".no_such_table")
	if err := other.Check(ctx); err == nil {
		t.Fatal("readiness of a missing table")
	}
	t.Log("measured: an account with INSERT on the host table only writes rows, absorbs duplicates and passes the readiness check")
}

func strPtr(s string) *string { return &s }
