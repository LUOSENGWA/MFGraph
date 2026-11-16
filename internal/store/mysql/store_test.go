package mysql_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/LUOSENGWA/MFGraph/internal/migrate"
	"github.com/LUOSENGWA/MFGraph/internal/ports/porttest"
	"github.com/LUOSENGWA/MFGraph/internal/store/mysql"
	"github.com/LUOSENGWA/MFGraph/migrations"
)

// openTestDB creates a fresh database on the server of
// MFGRAPH_TEST_MYSQL_DSN (MySQL 8.0 with `make test-mysql`, seekdb with
// `make test-seekdb`), applies the migrations and returns a pool on it.
// With MFGRAPH_TEST_DB_WAIT it first waits that long for the server.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("MFGRAPH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MFGRAPH_TEST_MYSQL_DSN not set (run `make test-mysql` or `make test-seekdb`)")
	}
	cfg, err := gomysql.ParseDSN(dsn)
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
	var version string
	_ = admin.QueryRow("SELECT VERSION()").Scan(&version)
	t.Logf("server version %s", version)
	name := fmt.Sprintf("mfgraph_storetest_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE DATABASE " + name + " CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP DATABASE IF EXISTS " + name) })
	cfg.DBName = name
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := migrate.RunAll(context.Background(), migrate.SQLTarget{DB: db}, []migrate.Plan{
		{Component: migrate.ComponentKernel, Source: migrations.Kernel()},
		{Component: migrate.ComponentMFGraph, Source: migrations.MySQL()},
	}, time.Now); err != nil {
		t.Fatal(err)
	}
	return db
}

var tables = []string{"projects", "principals", "workspaces", "workstreams", "branches", "snapshots", "snapshot_parents",
	"blobs", "upload_sessions", "upload_parts", "workspace_usage", "outbox_events", "idempotency_keys"}

// TestMySQLContract runs the repository contract tests (ports/porttest) on
// MySQL 8.0 or seekdb. The subtests share one migrated database whose
// tables are emptied before each of them.
func TestMySQLContract(t *testing.T) {
	db := openTestDB(t)
	porttest.Run(t, func(t *testing.T) porttest.Store {
		for _, table := range tables {
			if _, err := db.Exec("DELETE FROM " + table); err != nil {
				t.Fatal(err)
			}
		}
		return mysql.New(db)
	})
}
