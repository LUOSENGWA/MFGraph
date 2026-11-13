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

	"github.com/LUOSENGWA/MFGraph/internal/migrate"
	"github.com/LUOSENGWA/MFGraph/migrations"
)

// TestMySQL runs the real migrations against a MySQL server. It needs
// MFGRAPH_TEST_MYSQL_DSN (a go-sql-driver DSN of a user that may create and
// drop databases, e.g. "root:mfgraph@tcp(127.0.0.1:3306)/"); see
// `make test-mysql`. Every run uses a fresh, uniquely named database.
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
	if err := admin.PingContext(ctx); err != nil {
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
// utf8mb4_bin tables, ascii_bin ULID and hash columns.
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
		if coll != "ascii_bin" {
			t.Errorf("%s.%s %s has collation %q, want ascii_bin", table, col, typ, coll)
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
