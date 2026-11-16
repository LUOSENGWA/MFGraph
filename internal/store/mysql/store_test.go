package mysql_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/LUOSENGWA/MFGraph/internal/migrate"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
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

// TestMySQLLockErrors: a lock wait timeout and a deadlock surface as
// ports.ErrLockTimeout and ports.ErrDeadlock, both ports.ErrDBUnavailable
// (the API answers a retryable 503). MySQL reports a deadlock at once
// (1213); seekdb has no deadlock detection within a statement's time and
// reports a lock wait timeout (1205) instead.
func TestMySQLLockErrors(t *testing.T) {
	db := openTestDB(t)
	var version, name string
	if err := db.QueryRow("SELECT VERSION(), DATABASE()").Scan(&version, &name); err != nil {
		t.Fatal(err)
	}
	seekdb := strings.Contains(version, "OceanBase")
	cfg, err := gomysql.ParseDSN(os.Getenv("MFGRAPH_TEST_MYSQL_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.DBName = name
	cfg.Params = map[string]string{"innodb_lock_wait_timeout": "1"} // seconds
	if seekdb {
		cfg.Params = map[string]string{"ob_trx_lock_timeout": "1000000"} // microseconds
	}
	shortDB, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shortDB.Close() })
	store, short := mysql.New(db), mysql.New(shortDB)
	ctx := context.Background()
	for _, k := range []string{"k1", "k2"} {
		if err := store.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
			return store.InsertIdempotency(ctx, tx, ports.IdempotencyRecord{ActorRef: "a", Route: "r", Key: k, RequestHash: "h",
				Status: ports.IdempotencyInProgress, CreatedAtUs: 1, ExpiresAtUs: 2})
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Completing a record locks its row until the transaction ends.
	lock := func(s *mysql.Store, tx ports.Tx, key string) error {
		return s.CompleteIdempotency(ctx, tx, "a", "r", key, 1, []byte("{}"))
	}
	errRollback := errors.New("rolled back by the test")

	locked, release := make(chan struct{}), make(chan struct{})
	holder := make(chan error, 1)
	go func() {
		holder <- store.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
			if err := lock(store, tx, "k1"); err != nil {
				return err
			}
			close(locked)
			<-release
			return errRollback
		})
	}()
	<-locked
	start := time.Now()
	err = short.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error { return lock(short, tx, "k1") })
	close(release)
	if h := <-holder; !errors.Is(h, errRollback) {
		t.Fatalf("the transaction holding the lock: %v", h)
	}
	if !errors.Is(err, ports.ErrLockTimeout) || !errors.Is(err, ports.ErrDBUnavailable) {
		t.Fatalf("waiting for the lock: %v (after %v)", err, time.Since(start))
	}

	// Each of two transactions holds a row the other one wants.
	errs := make([]error, 2)
	order := [2][2]string{{"k1", "k2"}, {"k2", "k1"}}
	holding := make(chan struct{}, 2)
	proceed := make(chan struct{})
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = short.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
				if err := lock(short, tx, order[i][0]); err != nil {
					return err
				}
				holding <- struct{}{}
				<-proceed
				if err := lock(short, tx, order[i][1]); err != nil {
					return err
				}
				return errRollback
			})
		}(i)
	}
	<-holding
	<-holding
	close(proceed)
	wg.Wait()
	deadlocks, timeouts := 0, 0
	for _, err := range errs {
		switch {
		case errors.Is(err, ports.ErrDeadlock) && errors.Is(err, ports.ErrDBUnavailable):
			deadlocks++
		case errors.Is(err, ports.ErrLockTimeout):
			timeouts++
		case !errors.Is(err, errRollback):
			t.Fatalf("deadlock: unexpected error %v", err)
		}
	}
	if deadlocks+timeouts == 0 || !seekdb && deadlocks != 1 {
		t.Fatalf("deadlock on %s: %v", version, errs)
	}
	t.Logf("deadlock on %s: %d reported as deadlock, %d as lock wait timeout", version, deadlocks, timeouts)
}
