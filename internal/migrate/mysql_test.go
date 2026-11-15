package migrate_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/LUOSENGWA/MFGraph/internal/actorref"
	"github.com/LUOSENGWA/MFGraph/internal/extref"
	"github.com/LUOSENGWA/MFGraph/internal/migrate"
	"github.com/LUOSENGWA/MFGraph/internal/storagekey"
	"github.com/LUOSENGWA/MFGraph/migrations"
)

// TestMySQL runs the real migrations against a MySQL-protocol server: MySQL
// 8.0 (`make test-mysql`) or seekdb (`make test-seekdb`). It needs
// MFGRAPH_TEST_MYSQL_DSN (a go-sql-driver DSN of a user that may create and
// drop databases, e.g. "root:mfgraph@tcp(127.0.0.1:3306)/" or
// "root@tcp(127.0.0.1:2881)/"). Every run uses a fresh, uniquely named
// database. With MFGRAPH_TEST_DB_WAIT (a duration) it first waits that long
// for the server to answer queries.
func TestMySQL(t *testing.T) {
	dsn := os.Getenv("MFGRAPH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MFGRAPH_TEST_MYSQL_DSN not set (run `make test-mysql`)")
	}
	ctx := context.Background()
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	adminCfg := cfg.Clone()
	adminCfg.DBName = ""
	admin, err := sql.Open("mysql", adminCfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if err := waitForDB(ctx, admin); err != nil {
		t.Fatalf("connect: %v", err)
	}
	var version string
	if err := admin.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("server version %s", version)

	dbName := fmt.Sprintf("mfgraph_migtest_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+dbName+" CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+dbName) })
	cfg.DBName = dbName
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	target := migrate.SQLTarget{DB: db}
	plans := []migrate.Plan{
		{Component: migrate.ComponentKernel, Source: migrations.Kernel()},
		{Component: migrate.ComponentMFGraph, Source: migrations.MySQL()},
	}

	// 1. Empty database: everything is applied.
	rs, err := migrate.RunAll(ctx, target, plans, time.Now)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if got := append(rs[0].Applied, rs[1].Applied...); len(got) != 7 || len(rs[0].Skipped)+len(rs[1].Skipped) != 0 {
		t.Fatalf("first run applied %v", got)
	}
	tables := listTables(t, db, dbName)
	want := []string{"blobs", "branches", "idempotency_keys", "outbox_events", "principals", "projects", "schema_migrations",
		"snapshot_parents", "snapshots", "upload_parts", "upload_sessions", "workspace_usage", "workspaces", "workstreams"}
	if !reflect.DeepEqual(tables, want) {
		t.Fatalf("tables = %v\nwant %v", tables, want)
	}
	checkSchemaConventions(t, db, dbName)
	checkByteExactColumns(t, db, dbName)

	// 2. Second run: everything is skipped, nothing executed.
	rs, err = migrate.RunAll(ctx, target, plans, time.Now)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(rs[0].Applied)+len(rs[1].Applied) != 0 || len(rs[0].Skipped)+len(rs[1].Skipped) != 7 {
		t.Fatalf("second run: %+v", rs)
	}

	// 3. A modified applied file is refused before anything runs.
	tampered := copyFS(t, migrations.MySQL())
	tampered["0002_workstreams_branches.sql"].Data = append(tampered["0002_workstreams_branches.sql"].Data, []byte("-- edited\n")...)
	tampered["0009_new.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE IF NOT EXISTS must_not_exist (x INT) ENGINE=InnoDB;\n")}
	_, err = migrate.RunAll(ctx, target, []migrate.Plan{{Component: migrate.ComponentMFGraph, Source: tampered}}, time.Now)
	if !errors.Is(err, migrate.ErrChecksumMismatch) {
		t.Fatalf("tampered run: err = %v", err)
	}
	if contains(listTables(t, db, dbName), "must_not_exist") {
		t.Fatal("a pending migration ran after the checksum mismatch")
	}

	// 4. Interrupted run: statements executed but not recorded; rerunning is safe.
	if _, err := db.ExecContext(ctx, "DELETE FROM schema_migrations WHERE component = ? AND version = ?", "mfgraph", "0008_usage_outbox_idempotency"); err != nil {
		t.Fatal(err)
	}
	rs, err = migrate.RunAll(ctx, target, plans[1:], time.Now)
	if err != nil || !reflect.DeepEqual(rs[0].Applied, []string{"0008_usage_outbox_idempotency"}) {
		t.Fatalf("resume run: %+v %v", rs, err)
	}

	// 5. A gap filled later (e.g. 0005 added after 0006/0008 were applied).
	withGap := copyFS(t, migrations.MySQL())
	withGap["0005_gap_fill_test.sql"] = &fstest.MapFile{Data: []byte(
		"CREATE TABLE IF NOT EXISTS gap_fill_test (x INT) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;\n")}
	rs, err = migrate.RunAll(ctx, target, []migrate.Plan{{Component: migrate.ComponentMFGraph, Source: withGap}}, time.Now)
	if err != nil || !reflect.DeepEqual(rs[0].Applied, []string{"0005_gap_fill_test"}) {
		t.Fatalf("gap fill: %+v %v", rs, err)
	}

	// 6. A recorded migration whose file disappeared is refused.
	_, err = migrate.RunAll(ctx, target, []migrate.Plan{{Component: migrate.ComponentMFGraph, Source: migrations.MySQL()}}, time.Now)
	if !errors.Is(err, migrate.ErrUnknownVersion) {
		t.Fatalf("missing file: err = %v", err)
	}
}

func listTables(t *testing.T, db *sql.DB, schema string) []string {
	t.Helper()
	rows, err := db.Query("SELECT table_name FROM information_schema.tables WHERE table_schema = ?", schema)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// checkSchemaConventions verifies what the server actually created: InnoDB,
// utf8mb4_bin tables, utf8mb4_bin ULID and hash columns.
func checkSchemaConventions(t *testing.T, db *sql.DB, schema string) {
	t.Helper()
	rows, err := db.Query("SELECT table_name, engine, table_collation FROM information_schema.tables WHERE table_schema = ?", schema)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name, engine, coll string
		if err := rows.Scan(&name, &engine, &coll); err != nil {
			t.Fatal(err)
		}
		if engine != "InnoDB" || coll != "utf8mb4_bin" {
			t.Errorf("table %s: engine %s collation %s", name, engine, coll)
		}
	}
	rows.Close()
	rows, err = db.Query(`SELECT table_name, column_name, column_type, IFNULL(collation_name, '') FROM information_schema.columns
		WHERE table_schema = ? AND (column_type = 'char(26)' OR column_type = 'char(64)')`, schema)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var table, col, typ, coll string
		if err := rows.Scan(&table, &col, &typ, &coll); err != nil {
			t.Fatal(err)
		}
		n++
		if coll != "utf8mb4_bin" {
			t.Errorf("%s.%s %s has collation %q, want utf8mb4_bin", table, col, typ, coll)
		}
	}
	if n == 0 {
		t.Error("no ID/hash columns found")
	}
}

func copyFS(t *testing.T, fsys fs.FS) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		out[p] = &fstest.MapFile{Data: b}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func mysqlErrNo(err error) uint16 {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number
	}
	return 0
}

const (
	errDupEntry    = 1062 // ER_DUP_ENTRY
	errDataTooLong = 1406 // ER_DATA_TOO_LONG
)

// checkByteExactColumns measures, on the real server, what the VARBINARY
// columns and the new keys are for: names that differ only in trailing
// spaces are different rows, a 255-byte branch name fits and 256 bytes do
// not, storage prefixes that differ only in case collide through
// storage_prefix_key, an idempotency key is scoped by route, and
// root_tree_hash_algo defaults to sha256.
func checkByteExactColumns(t *testing.T, db *sql.DB, schema string) {
	t.Helper()
	ctx := context.Background()
	exec := func(q string, args ...any) error {
		_, err := db.ExecContext(ctx, q, args...)
		return err
	}
	const ws, wst, user = "01JD00000000000000000WS000", "01JD00000000000000000WT000", "01JD00000000000000000US000"
	n := 0
	branch := func(name string) error {
		n++
		return exec(`INSERT INTO branches (branch_id, workspace_id, project_id, workstream_id, branch_name, ref_name,
			created_by_ref, created_at_us, updated_at_us) VALUES (?, ?, ?, ?, ?, ?, ?, 0, 0)`,
			fmt.Sprintf("01JD0000000000000000BR%04d", n), ws, ws, wst, name, "workstreams/"+wst+"/"+name, user)
	}

	// 1. "dev" and "dev " are two branches; an exact duplicate is refused.
	if err := branch("dev"); err != nil {
		t.Fatal(err)
	}
	if err := branch("dev "); err != nil {
		t.Errorf(`branch "dev " next to "dev": %v`, err)
	}
	if err := branch("dev"); mysqlErrNo(err) != errDupEntry {
		t.Errorf(`a second branch "dev": err = %v, want duplicate entry`, err)
	}
	var rows int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM branches WHERE workspace_id = ? AND ref_name = ?",
		ws, "workstreams/"+wst+"/dev").Scan(&rows); err != nil || rows != 1 {
		t.Errorf("lookup of ref workstreams/<id>/dev: %d rows, %v; want exactly 1", rows, err)
	}
	t.Logf(`measured: branch names "dev" and "dev " coexist, a second "dev" is a duplicate entry, the lookup of ref .../dev matches %d row`, rows)

	// 2. 255-byte branch names fit (ASCII and 85 CJK characters), 256 bytes do not.
	for _, name := range []string{strings.Repeat("a", 255), strings.Repeat("\u4e2d", 85)} {
		if err := branch(name); err != nil {
			t.Errorf("%d-byte branch name: %v", len(name), err)
		}
	}
	err := branch(strings.Repeat("b", 256))
	if mysqlErrNo(err) != errDataTooLong {
		t.Errorf("256-byte branch name: err = %v, want data too long", err)
	}
	var longest int
	if err := db.QueryRowContext(ctx, "SELECT MAX(LENGTH(branch_name)), MAX(LENGTH(ref_name)) FROM branches").Scan(&longest, new(int)); err != nil || longest != 255 {
		t.Errorf("longest stored branch name: %d bytes, %v", longest, err)
	}
	t.Logf("measured: 255-byte branch names (ASCII, 85 CJK characters) are stored, their refs have %d bytes; 256 bytes: %v",
		len("workstreams/"+wst+"/")+255, err)

	// 3. Storage prefixes that differ only in case collide on one backend.
	workspace := func(id, backend, prefix string) error {
		return exec(`INSERT INTO workspaces (workspace_id, tenant_id, project_id, workspace_type, owner_ref,
			storage_backend_id, storage_prefix, storage_prefix_key, created_at_us, updated_at_us)
			VALUES (?, ?, ?, 'project', ?, ?, ?, ?, 0, 0)`, id, ws, ws, user, backend, prefix, storagekey.PrefixKey(prefix))
	}
	if err := workspace("01JD00000000000000000WA001", "default", "tenants/A/w"); err != nil {
		t.Fatal(err)
	}
	if err := workspace("01JD00000000000000000WA002", "default", "Tenants/a/W"); mysqlErrNo(err) != errDupEntry {
		t.Errorf("prefix differing only in case: err = %v, want duplicate entry", err)
	}
	if err := workspace("01JD00000000000000000WA003", "other", "Tenants/a/W"); err != nil {
		t.Errorf("the same prefix on another backend: %v", err)
	}
	// The longest valid prefix: 942 bytes in segments of at most 255.
	longestPrefix := strings.Join([]string{strings.Repeat("p", 255), strings.Repeat("q", 255), strings.Repeat("r", 255), strings.Repeat("s", 174)}, "/")
	if err := storagekey.ValidatePrefix(longestPrefix); err != nil || len(longestPrefix) != storagekey.MaxPrefixBytes {
		t.Fatalf("test prefix of %d bytes: %v", len(longestPrefix), err)
	}
	if err := workspace("01JD00000000000000000WA004", "default", longestPrefix); err != nil {
		t.Errorf("942-byte prefix: %v", err)
	}
	t.Log("measured: storage prefixes tenants/A/w and Tenants/a/W collide on one backend (storage_prefix_key), not across backends; a 942-byte prefix is stored")

	// 4. An idempotency key is scoped by actor and route.
	idem := func(route string) error {
		return exec(`INSERT INTO idempotency_keys (idempotency_key, actor_ref, route, request_hash, created_at_us, expires_at_us)
			VALUES ('k-1', ?, ?, ?, 0, 1)`, user, route, strings.Repeat("0", 64))
	}
	if err := idem("POST /v1/workspaces/{id}/commits"); err != nil {
		t.Fatal(err)
	}
	if err := idem("POST /v1/workspaces/{id}/branches"); err != nil {
		t.Errorf("same key on another route: %v", err)
	}
	if err := idem("POST /v1/workspaces/{id}/commits"); mysqlErrNo(err) != errDupEntry {
		t.Errorf("same key and route: err = %v, want duplicate entry", err)
	}

	// 5. root_tree_hash_algo defaults to sha256.
	if err := exec(`INSERT INTO snapshots (snapshot_id, workspace_id, project_id, author_ref, trigger_kind,
		root_tree_hash, created_at_us) VALUES (?, ?, ?, ?, 'manual', ?, 0)`, "01JD00000000000000000SN001", ws, ws, user,
		strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	var algo string
	if err := db.QueryRowContext(ctx, "SELECT root_tree_hash_algo FROM snapshots").Scan(&algo); err != nil || algo != "sha256" {
		t.Errorf("root_tree_hash_algo = %q, %v", algo, err)
	}

	checkActorRefs(t, db)

	// 6. The server created the byte columns as VARBINARY.
	want := map[string]string{
		"branches.branch_name": "varbinary(255)", "branches.ref_name": "varbinary(512)", "snapshots.ref_name": "varbinary(512)",
		"workstreams.default_branch_name": "varbinary(255)", "workspaces.scope_path": "varbinary(4096)",
		"snapshots.scope_path": "varbinary(4096)", "upload_sessions.path": "varbinary(4096)",
		"workspaces.storage_prefix": "varbinary(942)", "blobs.storage_key": "varbinary(1024)",
		"idempotency_keys.idempotency_key": "varbinary(128)",
		// Actor references.
		"workspaces.owner_ref": "varbinary(255)", "workstreams.owner_ref": "varbinary(255)",
		"workstreams.created_by_ref": "varbinary(255)", "branches.created_by_ref": "varbinary(255)",
		"snapshots.author_ref": "varbinary(255)", "snapshots.on_behalf_of_ref": "varbinary(255)",
		"upload_sessions.created_by_ref": "varbinary(255)", "idempotency_keys.actor_ref": "varbinary(255)",
		// External references.
		"snapshots.workflow_instance_id": "varbinary(255)",
	}
	got := map[string]string{}
	rs, err := db.QueryContext(ctx, `SELECT table_name, column_name, column_type FROM information_schema.columns
		WHERE table_schema = ? AND data_type = 'varbinary'`, schema)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	for rs.Next() {
		var table, col, typ string
		if err := rs.Scan(&table, &col, &typ); err != nil {
			t.Fatal(err)
		}
		got[table+"."+col] = typ
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("VARBINARY columns = %v\nwant %v", got, want)
	}
}

// checkActorRefs measures that actor references of the forms a host platform
// passes are stored exactly: a user name, a 253-character worker name, an
// @user:server ID (also one of 255 bytes) and a ULID go into
// snapshots.author_ref and idempotency_keys.actor_ref and read back
// byte for byte; "a" and "a " are two different values; 256 bytes do not fit.
func checkActorRefs(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	const ws = "01JD00000000000000000WS000"
	refs := []string{
		"manager",
		"worker-" + strings.Repeat("a", 243) + "-01",                         // 253 characters
		"@alice:example.com",                                                 // @user:server
		"@" + strings.Repeat("u", 255-len("@:example.com")) + ":example.com", // 255 bytes
		"01JD3X5Q8Z9W2V4T6R8P0N2M4K",                                         // ULID
		"a", "a ",                                                            // differ only by a trailing space
	}
	for i, ref := range refs {
		if err := actorref.Validate(ref); err != nil {
			t.Fatalf("test value %d: %v", i, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO snapshots (snapshot_id, workspace_id, project_id, author_ref,
			on_behalf_of_ref, trigger_kind, root_tree_hash, created_at_us) VALUES (?, ?, ?, ?, ?, 'manual', ?, 0)`,
			fmt.Sprintf("01JD00000000000000000SA%03d", i), ws, ws, ref, ref, strings.Repeat("e", 64)); err != nil {
			t.Errorf("snapshots.author_ref = %d-byte %q: %v", len(ref), ref, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO idempotency_keys (idempotency_key, actor_ref, route, request_hash,
			created_at_us, expires_at_us) VALUES ('k-actor', ?, 'POST /x', ?, 0, 1)`, ref, strings.Repeat("0", 64)); err != nil {
			t.Errorf("idempotency_keys.actor_ref = %d-byte %q: %v", len(ref), ref, err)
		}
	}
	for i, ref := range refs {
		var author, behalf string
		var n int
		if err := db.QueryRowContext(ctx, "SELECT author_ref, on_behalf_of_ref FROM snapshots WHERE snapshot_id = ?",
			fmt.Sprintf("01JD00000000000000000SA%03d", i)).Scan(&author, &behalf); err != nil || author != ref || behalf != ref {
			t.Errorf("read back %q: author %q, on behalf of %q, %v", ref, author, behalf, err)
		}
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM snapshots WHERE author_ref = ?", ref).Scan(&n); err != nil || n != 1 {
			t.Errorf("snapshots with author_ref %q: %d, %v; want exactly 1", ref, n, err)
		}
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM idempotency_keys WHERE actor_ref = ? AND idempotency_key = 'k-actor'",
			ref).Scan(&n); err != nil || n != 1 {
			t.Errorf("idempotency records of %q: %d, %v; want exactly 1", ref, n, err)
		}
	}
	_, err := db.ExecContext(ctx, `INSERT INTO idempotency_keys (idempotency_key, actor_ref, route, request_hash, created_at_us,
		expires_at_us) VALUES ('k-long', ?, 'POST /x', ?, 0, 1)`, strings.Repeat("x", actorref.MaxBytes+1), strings.Repeat("0", 64))
	if mysqlErrNo(err) != errDataTooLong {
		t.Errorf("256-byte actor_ref: err = %v, want data too long", err)
	}
	// An external reference generated by a host platform: 255 bytes, not a ULID.
	wf := "wf-" + strings.Repeat("r", extref.MaxBytes-3)
	if _, err := db.ExecContext(ctx, `INSERT INTO snapshots (snapshot_id, workspace_id, project_id, author_ref,
		workflow_instance_id, observed_event_seq, trigger_kind, root_tree_hash, created_at_us)
		VALUES ('01JD00000000000000000SW001', ?, ?, 'manager', ?, 7, 'manual', ?, 0)`, ws, ws, wf, strings.Repeat("e", 64)); err != nil {
		t.Errorf("255-byte workflow_instance_id: %v", err)
	}
	var got string
	if err := db.QueryRowContext(ctx, "SELECT workflow_instance_id FROM snapshots WHERE workflow_instance_id = ? AND observed_event_seq = 7",
		wf).Scan(&got); err != nil || got != wf {
		t.Errorf("read back workflow_instance_id: %d bytes, %v", len(got), err)
	}
	_, wfErr := db.ExecContext(ctx, `INSERT INTO snapshots (snapshot_id, workspace_id, project_id, author_ref,
		workflow_instance_id, trigger_kind, root_tree_hash, created_at_us) VALUES ('01JD00000000000000000SW002', ?, ?, 'manager', ?,
		'manual', ?, 0)`, ws, ws, wf+"r", strings.Repeat("e", 64))
	if mysqlErrNo(wfErr) != errDataTooLong {
		t.Errorf("256-byte workflow_instance_id: err = %v, want data too long", wfErr)
	}
	t.Logf("measured: a 255-byte workflow_instance_id that is not a ULID is stored and found again byte for byte; 256 bytes: %v", wfErr)
	t.Logf("measured: actor references manager, a 253-character worker name, @alice:example.com, a 255-byte @user:server ID "+
		"and a ULID are stored in snapshots.author_ref and idempotency_keys.actor_ref and read back exactly; \"a\" and \"a \" "+
		"are two values; a 256-byte reference: %v", err)
}

// waitForDB returns once the server answers SELECT 1. Without
// MFGRAPH_TEST_DB_WAIT it tries once; with it, it retries every two seconds
// for that long (seekdb needs a while to initialize a fresh data directory).
func waitForDB(ctx context.Context, db *sql.DB) error {
	var wait time.Duration
	if v := os.Getenv("MFGRAPH_TEST_DB_WAIT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("MFGRAPH_TEST_DB_WAIT: %w", err)
		}
		wait = d
	}
	deadline := time.Now().Add(wait)
	for {
		qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		var one int
		err := db.QueryRowContext(qctx, "SELECT 1").Scan(&one)
		cancel()
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(2 * time.Second)
	}
}
