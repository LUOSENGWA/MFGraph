package migrate_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/migrate"
)

// fakeTarget records executed statements and versions in memory.
type fakeTarget struct {
	applied map[string]map[string]string // component -> version -> checksum
	execs   []string
	failOn  string // fail an Exec whose statement contains this
}

func newFake() *fakeTarget { return &fakeTarget{applied: map[string]map[string]string{}} }

func (f *fakeTarget) EnsureVersionTable(context.Context) error { return nil }

func (f *fakeTarget) AppliedVersions(_ context.Context, c string) (map[string]string, error) {
	out := map[string]string{}
	for v, s := range f.applied[c] {
		out[v] = s
	}
	return out, nil
}

func (f *fakeTarget) Exec(_ context.Context, stmt string) error {
	if f.failOn != "" && strings.Contains(stmt, f.failOn) {
		return errors.New("boom")
	}
	f.execs = append(f.execs, stmt)
	return nil
}

func (f *fakeTarget) RecordVersion(_ context.Context, c, v, sum string, _ int64) error {
	if f.applied[c] == nil {
		f.applied[c] = map[string]string{}
	}
	f.applied[c][v] = sum
	return nil
}

var fixedNow = func() time.Time { return time.Unix(1700000000, 0) }

func src(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for k, v := range files {
		m[k] = &fstest.MapFile{Data: []byte(v)}
	}
	return m
}

func load(t *testing.T, files map[string]string) []migrate.Migration {
	t.Helper()
	ms, err := migrate.Load(src(files))
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func TestSplitStatements(t *testing.T) {
	in := "-- header comment;\n\nCREATE TABLE a (\n  x INT -- inline\n);\n-- between\nCREATE TABLE b (y INT);  \r\n\n-- trailing comment only\n"
	got, err := migrate.SplitStatements(in)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-- header comment;\nCREATE TABLE a (\n  x INT -- inline\n);", // blank lines before code are dropped
		"-- between\nCREATE TABLE b (y INT);",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if _, err := migrate.SplitStatements("CREATE TABLE a (x INT)"); !errors.Is(err, migrate.ErrInvalidSource) {
		t.Fatalf("missing ';': err = %v", err)
	}
}

func TestLoadOrderingAndNames(t *testing.T) {
	ms := load(t, map[string]string{
		"0008_c.sql": "CREATE TABLE c (x INT);\n",
		"0001_a.sql": "CREATE TABLE a (x INT);\n",
		"0002_b.sql": "CREATE TABLE b (x INT);\n",
		"README.md":  "ignored",
	})
	var versions []string
	for _, m := range ms {
		versions = append(versions, m.Version)
	}
	if !reflect.DeepEqual(versions, []string{"0001_a", "0002_b", "0008_c"}) {
		t.Fatalf("versions %v", versions)
	}
	if ms[0].Number != 1 || len(ms[0].Checksum) != 64 {
		t.Fatalf("migration %+v", ms[0])
	}
	for _, bad := range []map[string]string{
		{"1_a.sql": "x;"},
		{"0001_A.sql": "x;"},
		{"0001-a.sql": "x;"},
		{"0001_a.sql": "x;", "0001_b.sql": "y;"},
		{"0001_" + strings.Repeat("a", 60) + ".sql": "x;"},
	} {
		if _, err := migrate.Load(src(bad)); !errors.Is(err, migrate.ErrInvalidSource) {
			t.Errorf("Load(%v) err = %v", bad, err)
		}
	}
}

func TestRunAppliesThenSkips(t *testing.T) {
	ctx := context.Background()
	files := map[string]string{
		"0001_a.sql": "CREATE TABLE IF NOT EXISTS a (x INT);\nCREATE TABLE IF NOT EXISTS a2 (x INT);\n",
		"0004_b.sql": "CREATE TABLE IF NOT EXISTS b (x INT);\n",
	}
	f := newFake()
	res, err := migrate.Run(ctx, f, "mfgraph", load(t, files), fixedNow)
	if err != nil || !reflect.DeepEqual(res.Applied, []string{"0001_a", "0004_b"}) || len(res.Skipped) != 0 {
		t.Fatalf("first run: %+v %v", res, err)
	}
	if len(f.execs) != 3 {
		t.Fatalf("executed %d statements, want 3", len(f.execs))
	}
	res, err = migrate.Run(ctx, f, "mfgraph", load(t, files), fixedNow)
	if err != nil || len(res.Applied) != 0 || !reflect.DeepEqual(res.Skipped, []string{"0001_a", "0004_b"}) {
		t.Fatalf("second run: %+v %v", res, err)
	}
	if len(f.execs) != 3 {
		t.Fatal("second run executed statements")
	}
	// Components are independent.
	res, err = migrate.Run(ctx, f, "kernel", load(t, files), fixedNow)
	if err != nil || len(res.Applied) != 2 {
		t.Fatalf("other component: %+v %v", res, err)
	}
}

func TestRunGapFilledLater(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	base := map[string]string{
		"0001_a.sql": "CREATE TABLE IF NOT EXISTS a (x INT);\n",
		"0006_c.sql": "CREATE TABLE IF NOT EXISTS c (x INT);\n",
	}
	if _, err := migrate.Run(ctx, f, "mfgraph", load(t, base), fixedNow); err != nil {
		t.Fatal(err)
	}
	base["0005_b.sql"] = "CREATE TABLE IF NOT EXISTS b (x INT);\n"
	res, err := migrate.Run(ctx, f, "mfgraph", load(t, base), fixedNow)
	if err != nil || !reflect.DeepEqual(res.Applied, []string{"0005_b"}) {
		t.Fatalf("gap fill: %+v %v", res, err)
	}
}

func TestRunRejectsTamperingBeforeExecuting(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	files := map[string]string{"0001_a.sql": "CREATE TABLE IF NOT EXISTS a (x INT);\n"}
	if _, err := migrate.Run(ctx, f, "mfgraph", load(t, files), fixedNow); err != nil {
		t.Fatal(err)
	}
	files["0001_a.sql"] += "-- edited\n"
	files["0002_b.sql"] = "CREATE TABLE IF NOT EXISTS b (x INT);\n"
	before := len(f.execs)
	_, err := migrate.Run(ctx, f, "mfgraph", load(t, files), fixedNow)
	if !errors.Is(err, migrate.ErrChecksumMismatch) {
		t.Fatalf("err = %v", err)
	}
	if len(f.execs) != before {
		t.Fatal("pending migrations ran despite the checksum mismatch")
	}
	delete(files, "0001_a.sql")
	if _, err := migrate.Run(ctx, f, "mfgraph", load(t, files), fixedNow); !errors.Is(err, migrate.ErrUnknownVersion) {
		t.Fatalf("removed file: err = %v", err)
	}
}

func TestRunStopsOnFailureAndResumes(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	f.failOn = "a2"
	files := map[string]string{
		"0001_a.sql": "CREATE TABLE IF NOT EXISTS a (x INT);\nCREATE TABLE IF NOT EXISTS a2 (x INT);\n",
		"0002_b.sql": "CREATE TABLE IF NOT EXISTS b (x INT);\n",
	}
	res, err := migrate.Run(ctx, f, "mfgraph", load(t, files), fixedNow)
	if err == nil || len(res.Applied) != 0 || !strings.Contains(err.Error(), "0001_a statement 2") {
		t.Fatalf("expected failure in 0001_a: %+v %v", res, err)
	}
	if len(f.applied["mfgraph"]) != 0 {
		t.Fatal("failed migration was recorded")
	}
	f.failOn = ""
	res, err = migrate.Run(ctx, f, "mfgraph", load(t, files), fixedNow)
	if err != nil || !reflect.DeepEqual(res.Applied, []string{"0001_a", "0002_b"}) {
		t.Fatalf("resume: %+v %v", res, err)
	}
}

func TestRunAll(t *testing.T) {
	f := newFake()
	rs, err := migrate.RunAll(context.Background(), f, []migrate.Plan{
		{Component: migrate.ComponentKernel, Source: src(map[string]string{"0000_k.sql": "CREATE TABLE IF NOT EXISTS k (x INT);\n"})},
		{Component: migrate.ComponentMFGraph, Source: src(map[string]string{"0001_m.sql": "CREATE TABLE IF NOT EXISTS m (x INT);\n"})},
	}, fixedNow)
	if err != nil || len(rs) != 2 || rs[0].Applied[0] != "0000_k" || rs[1].Applied[0] != "0001_m" {
		t.Fatalf("RunAll: %+v %v", rs, err)
	}
	if _, err := migrate.RunAll(context.Background(), f, []migrate.Plan{{Component: "x", Source: src(map[string]string{"bad.sql": ""})}}, fixedNow); !errors.Is(err, migrate.ErrInvalidSource) {
		t.Fatalf("bad source: %v", err)
	}
}
