package outbox_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/LUOSENGWA/MFGraph/internal/outbox"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/ports/memory"
)

var ctx = context.Background()

// fakeSink is a host table in memory: rows by event_id, in insertion order.
type fakeSink struct {
	mu       sync.Mutex
	rows     []outbox.Row
	byID     map[string]bool
	attempts []string           // event IDs of every Insert call
	fail     func(string) error // by event_id; nil = accept
	index    outbox.IndexState
	indexErr error
}

func newSink() *fakeSink { return &fakeSink{byID: map[string]bool{}, index: outbox.IndexUnique} }

func (s *fakeSink) Insert(_ context.Context, r outbox.Row) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts = append(s.attempts, r.EventID)
	if s.fail != nil {
		if err := s.fail(r.EventID); err != nil {
			return err
		}
	}
	if !s.byID[r.EventID] { // the unique key: a duplicate is a success
		s.byID[r.EventID] = true
		s.rows = append(s.rows, r)
	}
	return nil
}

func (s *fakeSink) Check(context.Context) error { return nil }

func (s *fakeSink) UniqueEventIDIndex(context.Context) (outbox.IndexState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.index, s.indexErr
}

func (s *fakeSink) setFail(f func(string) error) { s.mu.Lock(); s.fail = f; s.mu.Unlock() }

func (s *fakeSink) kinds() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.rows {
		out = append(out, strings.TrimPrefix(r.EventKind, "mfgraph."))
	}
	return strings.Join(out, ",")
}

func (s *fakeSink) count() (rows, attempts int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rows), len(s.attempts)
}

// envelope returns a valid envelope with a derived-looking event ID.
func envelope(n int, actor string) []byte {
	b, _ := json.Marshal(map[string]any{"v": 1, "event_id": fmt.Sprintf("%064x", n), "occurred_at": "2026-11-16T08:00:00.000123Z",
		"project_id": "01JD3X5Q8Z9W2V4T6R8P0N2P01", "actor": map[string]any{"type": "human", "id": actor}, "data": map[string]any{}})
	return b
}

// appendEvents commits events (kind, time, ID suffix) in one transaction.
func appendEvents(t *testing.T, s *memory.Store, evs ...ports.Event) {
	t.Helper()
	err := s.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		for _, ev := range evs {
			if err := s.Append(ctx, tx, ev); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func ev(n int, kind string, at int64) ports.Event {
	return ports.Event{ID: fmt.Sprintf("01JD3X5Q8Z9W2V4T6R8P0N%04d", 9999-n), Type: kind, WorkspaceID: "01JD3X5Q8Z9W2V4T6R8P0N2W01",
		AggregateID: "01JD3X5Q8Z9W2V4T6R8P0N2W01", Payload: envelope(n, "manager"), CreatedAtUs: at}
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestFlushOrderAndFirstFailure: events go out in outbox order (time, then
// row ID); the first one that fails stops the batch and is not skipped.
func TestFlushOrderAndFirstFailure(t *testing.T) {
	store, sink := memory.NewStore(), newSink()
	// IDs sort against the times: the order must come from created_at_us.
	appendEvents(t, store, ev(1, ports.EventWorkspaceCreated, 100), ev(2, ports.EventBranchCreated, 101))
	appendEvents(t, store, ev(3, ports.EventCommitCreated, 200), ev(4, ports.EventBranchUpdated, 201))
	appendEvents(t, store, ev(5, ports.EventSnapshotRestored, 300), ev(6, ports.EventBranchUpdated, 301))
	f := outbox.New(store, sink, outbox.Options{Logger: quiet()})
	down := errors.New("host table unavailable")
	sink.setFail(func(id string) error {
		if id == fmt.Sprintf("%064x", 3) {
			return down
		}
		return nil
	})
	n, err := f.Flush(ctx)
	if !errors.Is(err, down) || n != 2 || sink.kinds() != "workspace_created,branch_created" {
		t.Fatalf("first flush: %d %v, delivered %s", n, err, sink.kinds())
	}
	if _, attempts := sink.count(); attempts != 3 {
		t.Fatalf("attempts %d: the events after the failure must not be tried", attempts)
	}
	failed := ev(3, "", 0).ID
	if d, _ := store.EventDelivery(ctx, failed); d.Status != ports.EventPending || d.RetryCount != 1 || d.NextRetryAtUs == 0 {
		t.Fatalf("the failed event: %+v", d)
	}
	if d, _ := store.EventDelivery(ctx, ev(1, "", 0).ID); d.Status != ports.EventPublished || d.PublishedAtUs == 0 {
		t.Fatalf("a delivered event: %+v", d)
	}
	// Still failing: the same event again, nothing after it.
	if n, err := f.Flush(ctx); n != 0 || err == nil {
		t.Fatalf("second flush: %d %v", n, err)
	}
	if d, _ := store.EventDelivery(ctx, failed); d.RetryCount != 2 {
		t.Fatalf("retries: %+v", d)
	}
	sink.setFail(nil)
	if n, err := f.Flush(ctx); n != 4 || err != nil {
		t.Fatalf("after recovery: %d %v", n, err)
	}
	if got := sink.kinds(); got != "workspace_created,branch_created,commit_created,branch_updated,snapshot_restored,branch_updated" {
		t.Fatalf("delivered %s", got)
	}
	if n, err := f.Flush(ctx); n != 0 || err != nil {
		t.Fatalf("nothing left: %d %v", n, err)
	}
}

// TestRedeliveryIsDeduplicated: an event written to the host but not marked
// published locally is written again, and the host keeps one row.
func TestRedeliveryIsDeduplicated(t *testing.T) {
	sink := newSink()
	store := &forgetful{Store: memory.NewStore()}
	appendEvents(t, store.Store, ev(1, ports.EventWorkspaceCreated, 1), ev(2, ports.EventBranchCreated, 2))
	f := outbox.New(store, sink, outbox.Options{Logger: quiet()})
	store.failMark = true
	if n, err := f.Flush(ctx); n != 0 || err == nil {
		t.Fatalf("flush with a failing mark: %d %v", n, err)
	}
	store.failMark = false
	if n, err := f.Flush(ctx); n != 2 || err != nil {
		t.Fatalf("redelivery: %d %v", n, err)
	}
	rows, attempts := sink.count()
	if rows != 2 || attempts != 3 || sink.kinds() != "workspace_created,branch_created" {
		t.Fatalf("host rows %d, attempts %d: %s", rows, attempts, sink.kinds())
	}
}

// forgetful fails MarkPublished on request.
type forgetful struct {
	*memory.Store
	failMark bool
}

func (f *forgetful) MarkPublished(ctx context.Context, id string, at int64) error {
	if f.failMark {
		return errors.New("local database unavailable")
	}
	return f.Store.MarkPublished(ctx, id, at)
}

// TestBadEnvelopeIsNotSkipped: an event whose payload is no envelope stops
// the delivery; the events after it wait.
func TestBadEnvelopeIsNotSkipped(t *testing.T) {
	store, sink := memory.NewStore(), newSink()
	bad := ev(1, ports.EventWorkspaceCreated, 1)
	bad.Payload = []byte(`{"schema_version":1}`)
	appendEvents(t, store, bad, ev(2, ports.EventBranchCreated, 2))
	f := outbox.New(store, sink, outbox.Options{Logger: quiet()})
	if n, err := f.Flush(ctx); n != 0 || !errors.Is(err, outbox.ErrBadEnvelope) {
		t.Fatalf("flush: %d %v", n, err)
	}
	if rows, attempts := sink.count(); rows != 0 || attempts != 0 {
		t.Fatalf("host rows %d, attempts %d", rows, attempts)
	}
}

// TestRowOf: the columns of a host row.
func TestRowOf(t *testing.T) {
	ref := "@" + strings.Repeat("u", 255-len("@:example.com")) + ":example.com" // 255 bytes
	e := ev(7, ports.EventCommitCreated, 5)
	e.Payload = envelope(7, ref)
	now := time.Date(2026, 11, 16, 9, 30, 0, 456000, time.FixedZone("UTC+8", 8*3600))
	r, err := outbox.RowOf(e, now)
	if err != nil {
		t.Fatal(err)
	}
	if r.EventID != fmt.Sprintf("%064x", 7) || r.EventKind != ports.EventCommitCreated || r.ScopeID != "01JD3X5Q8Z9W2V4T6R8P0N2P01" ||
		r.ActorType == nil || *r.ActorType != "human" || r.ActorID != ref || string(r.Payload) != string(e.Payload) ||
		!r.CreatedAt.Equal(time.Date(2026, 11, 16, 8, 0, 0, 123000, time.UTC)) || r.CreatedAt.Location() != time.UTC ||
		!r.AvailableAt.Equal(now) || r.AvailableAt.Location() != time.UTC || !r.UpdatedAt.Equal(now) {
		t.Fatalf("row %+v", r)
	}
	// A standalone envelope has no actor type: NULL.
	b, _ := json.Marshal(map[string]any{"v": 1, "event_id": strings.Repeat("a", 64), "occurred_at": "2026-11-16T08:00:00.000000Z",
		"project_id": "01JD3X5Q8Z9W2V4T6R8P0N2P01", "actor": map[string]any{"type": nil, "id": "alice"}, "data": map[string]any{}})
	e.Payload = b
	if r, err := outbox.RowOf(e, now); err != nil || r.ActorType != nil {
		t.Fatalf("untyped actor: %+v %v", r, err)
	}
	for name, payload := range map[string]string{
		"not JSON":       "{",
		"version 2":      `{"v":2,"event_id":"` + strings.Repeat("a", 64) + `"}`,
		"short ID":       `{"v":1,"event_id":"abc","occurred_at":"2026-11-16T08:00:00Z","project_id":"01JD3X5Q8Z9W2V4T6R8P0N2P01","actor":{"id":"a"}}`,
		"bad project":    `{"v":1,"event_id":"` + strings.Repeat("a", 64) + `","occurred_at":"2026-11-16T08:00:00Z","project_id":"p","actor":{"id":"a"}}`,
		"no actor":       `{"v":1,"event_id":"` + strings.Repeat("a", 64) + `","occurred_at":"2026-11-16T08:00:00Z","project_id":"01JD3X5Q8Z9W2V4T6R8P0N2P01","actor":{"id":""}}`,
		"bad actor type": `{"v":1,"event_id":"` + strings.Repeat("a", 64) + `","occurred_at":"2026-11-16T08:00:00Z","project_id":"01JD3X5Q8Z9W2V4T6R8P0N2P01","actor":{"type":"Human","id":"a"}}`,
		"bad time":       `{"v":1,"event_id":"` + strings.Repeat("a", 64) + `","occurred_at":"yesterday","project_id":"01JD3X5Q8Z9W2V4T6R8P0N2P01","actor":{"id":"a"}}`,
	} {
		e.Payload = []byte(payload)
		if _, err := outbox.RowOf(e, now); !errors.Is(err, outbox.ErrBadEnvelope) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestRunNotifyAndBackoff: Notify delivers at once; after a failure the
// whole forwarder waits, doubling up to the maximum, and Notify does not
// shorten the wait.
func TestRunNotifyAndBackoff(t *testing.T) {
	store, sink := memory.NewStore(), newSink()
	f := outbox.New(store, sink, outbox.Options{Logger: quiet(), Interval: time.Hour, MinBackoff: 40 * time.Millisecond,
		MaxBackoff: 80 * time.Millisecond})
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { f.Run(runCtx); close(done) }()
	defer func() { cancel(); <-done }()

	waitRows := func(n int) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if rows, _ := sink.count(); rows >= n {
				return
			}
		}
		rows, _ := sink.count()
		t.Fatalf("%d host rows, want %d", rows, n)
	}
	time.Sleep(20 * time.Millisecond) // the first, empty round
	appendEvents(t, store, ev(1, ports.EventWorkspaceCreated, 1))
	f.Notify()
	waitRows(1) // long before the interval of an hour

	sink.setFail(func(string) error { return errors.New("down") })
	appendEvents(t, store, ev(2, ports.EventBranchCreated, 2))
	start := time.Now()
	f.Notify()
	for i := 0; i < 20; i++ { // wake-ups during the backoff change nothing
		f.Notify()
		time.Sleep(10 * time.Millisecond)
	}
	_, attempts := sink.count()
	// 200 ms: the attempt at once, then waits of 40, 80, 80 ms: about 3-4
	// attempts, never one per wake-up.
	if attempts < 3 || attempts > 6 {
		t.Fatalf("%d attempts in %v with backoff 40-80 ms", attempts, time.Since(start))
	}
	sink.setFail(nil)
	waitRows(2)
	if d, _ := store.EventDelivery(ctx, ev(2, "", 0).ID); d.RetryCount < 2 || d.Status != ports.EventPublished {
		t.Fatalf("delivery of the retried event: %+v", d)
	}
}

// syncBuffer is a log destination that tests read while the forwarder runs.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// silentPort accepts TCP connections and never answers, like the review's
// ops port that accepts a connection and sends nothing back.
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

// TestCallTimeout: a host database that accepts the connection and never
// answers. Every call of the forwarder has a time limit: the write fails
// after it, the failure is recorded as a retry of the event, logged, and
// followed by the backoff.
func TestCallTimeout(t *testing.T) {
	db, err := sql.Open("mysql", "ops:ops-pass@tcp("+silentPort(t)+")/ops")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sink, err := outbox.NewSQLSink(db, "ops.module_outbox")
	if err != nil {
		t.Fatal(err)
	}
	store := memory.NewStore()
	appendEvents(t, store, ev(1, ports.EventWorkspaceCreated, 1))
	logs := &syncBuffer{}
	f := outbox.New(store, sink, outbox.Options{Logger: slog.New(slog.NewTextHandler(logs, nil)), CallTimeout: 300 * time.Millisecond,
		MinBackoff: 50 * time.Millisecond, MaxBackoff: 50 * time.Millisecond, Interval: time.Hour})
	start := time.Now()
	n, err := f.Flush(ctx)
	took := time.Since(start)
	if n != 0 || err == nil || !strings.Contains(err.Error(), "write the host's outbox table: no answer within 300ms") ||
		took < 250*time.Millisecond || took > 5*time.Second {
		t.Fatalf("flush against a silent database: %d %v after %v", n, err, took)
	}
	if d, _ := store.EventDelivery(ctx, ev(1, "", 0).ID); d.RetryCount != 1 || d.Status != ports.EventPending {
		t.Fatalf("delivery after the timeout: %+v", d)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { f.Run(runCtx); close(done) }()
	time.Sleep(1200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop within 2 s of the cancellation while a call was waiting")
	}
	if d, _ := store.EventDelivery(ctx, ev(1, "", 0).ID); d.RetryCount < 3 {
		t.Fatalf("retries after 1.2 s of timeouts of 300 ms: %+v", d)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "no answer within 300ms") ||
		strings.Contains(logs.String(), "ops-pass") {
		t.Fatalf("log:\n%s", logs.String())
	}
}

// clock is a time source the tests move by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }
func countOf(s, sub string) int      { return strings.Count(s, sub) }
func lastLine(s, sub string) (l string) {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, sub) {
			l = line
		}
	}
	return l
}

// TestStallReport: an event that keeps failing stalls delivery. After the
// configured number of failures in a row (or time) the forwarder logs one
// ERROR line with the backlog, the event and the error, again only after
// the report interval; Status reports the stall; once delivery moves on, an
// INFO line ends it. The time rule and a skipped event are covered too.
func TestStallReport(t *testing.T) {
	store, sink := memory.NewStore(), newSink()
	logs := &syncBuffer{}
	clk := &clock{t: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}
	f := outbox.New(store, sink, outbox.Options{Logger: slog.New(slog.NewTextHandler(logs, nil)), Now: clk.now,
		StallFailures: 3, StallAfter: time.Hour, StallReportEvery: 10 * time.Minute})
	appendEvents(t, store, ev(1, ports.EventWorkspaceCreated, 1_000_000), ev(2, ports.EventBranchCreated, 1_000_001))
	appendEvents(t, store, ev(3, ports.EventCommitCreated, 2_000_000))
	tooLong := errors.New("Error 1406 (22001): Data too long for column 'actor_id' at row 1")
	sink.setFail(func(id string) error {
		if id == fmt.Sprintf("%064x", 1) {
			return tooLong
		}
		return nil
	})
	first := clk.now()
	for i := 1; i <= 2; i++ {
		if n, err := f.Flush(ctx); n != 0 || err == nil {
			t.Fatalf("flush %d: %d %v", i, n, err)
		}
		clk.add(time.Second)
	}
	if st, err := f.Status(ctx); err != nil || st.Stalled || st.Pending != 3 || !st.OldestPendingAt.Equal(time.UnixMicro(1_000_000).UTC()) {
		t.Fatalf("status after 2 failures: %+v %v", st, err)
	}
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("ERROR before the threshold:\n%s", logs.String())
	}
	_, _ = f.Flush(ctx) // the third failure in a row: stalled
	st, err := f.Status(ctx)
	if err != nil || !st.Stalled || !st.StalledSince.Equal(first) || st.LastError == "" || !strings.Contains(st.LastError, "Data too long") ||
		st.Pending != 3 {
		t.Fatalf("status when stalled: %+v %v", st, err)
	}
	line := lastLine(logs.String(), "level=ERROR")
	for _, want := range []string{"is stalled", "event=" + ev(1, "", 0).ID, "event_id=" + fmt.Sprintf("%064x", 1),
		"event_kind=mfgraph.workspace_created", "failures=3", "pending=3", "oldest_pending_at=1970-01-01T00:00:01Z",
		"stalled_since=2026-11-17T09:00:00Z", "Data too long"} {
		if !strings.Contains(line, want) {
			t.Fatalf("ERROR line lacks %q:\n%s", want, line)
		}
	}
	// More failures within the report interval: no further ERROR.
	for i := 0; i < 5; i++ {
		clk.add(time.Minute)
		_, _ = f.Flush(ctx)
	}
	if n := countOf(logs.String(), "level=ERROR"); n != 1 {
		t.Fatalf("%d ERROR lines within the report interval", n)
	}
	clk.add(5 * time.Minute) // 10 minutes after the first ERROR
	_, _ = f.Flush(ctx)
	if n := countOf(logs.String(), "level=ERROR"); n != 2 {
		t.Fatalf("%d ERROR lines after the report interval, want 2", n)
	}
	// Repaired: delivery resumes with one INFO line.
	sink.setFail(nil)
	if n, err := f.Flush(ctx); n != 3 || err != nil {
		t.Fatalf("after the repair: %d %v", n, err)
	}
	if st, _ := f.Status(ctx); st.Stalled || st.Pending != 0 || !st.OldestPendingAt.IsZero() {
		t.Fatalf("status after the repair: %+v", st)
	}
	if n := countOf(logs.String(), "delivery to the host's outbox table resumed"); n != 1 ||
		!strings.Contains(lastLine(logs.String(), "resumed"), "level=INFO") {
		t.Fatalf("recovery lines:\n%s", logs.String())
	}

	// The time rule: one failure that lasts longer than StallAfter.
	f2 := outbox.New(store, sink, outbox.Options{Logger: quiet(), Now: clk.now, StallFailures: 100, StallAfter: time.Minute})
	appendEvents(t, store, ev(4, ports.EventBranchUpdated, 3_000_000), ev(5, ports.EventBranchUpdated, 3_000_001))
	sink.setFail(func(id string) error {
		if id == fmt.Sprintf("%064x", 4) {
			return errors.New("down")
		}
		return nil
	})
	_, _ = f2.Flush(ctx)
	if st, _ := f2.Status(ctx); st.Stalled {
		t.Fatalf("stalled after one failure: %+v", st)
	}
	clk.add(61 * time.Second)
	if st, _ := f2.Status(ctx); !st.Stalled || st.LastError == "" {
		t.Fatalf("not stalled after a minute of failures: %+v", st)
	}
	// The operator skips the event: the next one is delivered, the skipped
	// one stays in the outbox and is never delivered.
	store.SetEventStatus(ev(4, "", 0).ID, ports.EventSkipped)
	if n, err := f2.Flush(ctx); n != 1 || err != nil {
		t.Fatalf("after the skip: %d %v", n, err)
	}
	if d, _ := store.EventDelivery(ctx, ev(4, "", 0).ID); d.Status != ports.EventSkipped {
		t.Fatalf("skipped event: %+v", d)
	}
	if st, _ := f2.Status(ctx); st.Stalled || st.Pending != 0 {
		t.Fatalf("status after the skip: %+v", st)
	}
	if strings.Contains(sink.kinds(), "branch_updated,branch_updated") {
		t.Fatalf("the skipped event was delivered: %s", sink.kinds())
	}
}

// TestStallErrorIsClipped: the error that Status reports is valid UTF-8 of
// at most MaxErrorBytes bytes.
func TestStallErrorIsClipped(t *testing.T) {
	store, sink := memory.NewStore(), newSink()
	f := outbox.New(store, sink, outbox.Options{Logger: quiet(), StallFailures: 1})
	appendEvents(t, store, ev(1, ports.EventWorkspaceCreated, 1))
	sink.setFail(func(string) error { return errors.New(strings.Repeat("\u00e9", 400) + "\xff") })
	_, _ = f.Flush(ctx)
	st, _ := f.Status(ctx)
	if !st.Stalled || len(st.LastError) > outbox.MaxErrorBytes || len(st.LastError) < outbox.MaxErrorBytes-3 ||
		!utf8.ValidString(st.LastError) {
		t.Fatalf("last error of %d bytes, valid %v", len(st.LastError), utf8.ValidString(st.LastError))
	}
}

// TestCheckSinkIndex: CheckSink logs an ERROR when the host table has no
// unique index on event_id and a WARN when the account cannot see the
// indexes, without failing the readiness check; a state is logged when it
// changes and otherwise once per report interval.
func TestCheckSinkIndex(t *testing.T) {
	sink := newSink()
	logs := &syncBuffer{}
	clk := &clock{t: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}
	f := outbox.New(memory.NewStore(), sink, outbox.Options{Logger: slog.New(slog.NewTextHandler(logs, nil)), Now: clk.now,
		StallReportEvery: 5 * time.Minute})
	check := func() {
		t.Helper()
		if err := f.CheckSink(ctx); err != nil {
			t.Fatal(err)
		}
	}
	check()
	if logs.String() != "" {
		t.Fatalf("logged with a unique index:\n%s", logs.String())
	}
	sink.mu.Lock()
	sink.index = outbox.IndexMissing
	sink.mu.Unlock()
	for i := 0; i < 3; i++ {
		check()
		clk.add(time.Minute)
	}
	if n := countOf(logs.String(), "no unique index on event_id"); n != 1 || !strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("%d lines:\n%s", n, logs.String())
	}
	clk.add(3 * time.Minute)
	check()
	if n := countOf(logs.String(), "no unique index on event_id"); n != 2 {
		t.Fatalf("%d lines after the report interval", n)
	}
	sink.mu.Lock()
	sink.index = outbox.IndexUnknown
	sink.mu.Unlock()
	check()
	if !strings.Contains(lastLine(logs.String(), "cannot see the indexes"), "level=WARN") {
		t.Fatalf("no WARN for invisible indexes:\n%s", logs.String())
	}
}
