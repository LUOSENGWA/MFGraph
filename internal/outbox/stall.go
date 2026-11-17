package outbox

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

// Defaults of the stall report (Options).
const (
	// DefaultStallFailures and DefaultStallAfter: delivery is stalled when
	// the event at the head of the queue failed this many times in a row,
	// or has been failing for this long.
	DefaultStallFailures = 5
	DefaultStallAfter    = 5 * time.Minute
	// DefaultStallReportEvery: while delivery is stalled, at most one ERROR
	// line per this interval.
	DefaultStallReportEvery = 5 * time.Minute
	// MaxErrorBytes bounds the error text that Status reports.
	MaxErrorBytes = 512
)

// stall is the record of the failures of the event at the head of the
// queue.
type stall struct {
	id, eventID, kind string    // the event; id "" when the local outbox could not be read
	since             time.Time // its first failure
	failures          int       // in a row
	lastErr           string
	reported          time.Time // the last ERROR line; zero: none yet
}

// Status is the state of delivery, for /readyz.
type Status struct {
	Pending         int64     // events not delivered yet
	OldestPendingAt time.Time // when the oldest of them occurred; zero when none
	Stalled         bool      // see DefaultStallFailures
	StalledSince    time.Time // when stalled: the first failure of the event at the head of the queue
	LastError       string    // when stalled: its last error, at most MaxErrorBytes
}

// stalledLocked reports whether delivery is stalled at now (f.mu held).
func (f *Forwarder) stalledLocked(now time.Time) bool {
	return f.st.failures >= f.o.StallFailures || f.st.failures > 0 && now.Sub(f.st.since) >= f.o.StallAfter
}

// failed records a failure of the event at the head of the queue (ev nil:
// the local outbox could not be read). When delivery is stalled it logs an
// ERROR line with the backlog, the event and the error, at most once per
// StallReportEvery.
func (f *Forwarder) failed(ctx context.Context, ev *ports.Event, eventID string, err error) {
	now := f.o.Now()
	id, kind := "", ""
	if ev != nil {
		id, kind = ev.ID, ev.Type
	}
	f.mu.Lock()
	prev := f.st
	if f.st.failures == 0 || f.st.id != id {
		f.st = stall{id: id, eventID: eventID, kind: kind, since: now}
	}
	f.st.failures++
	f.st.lastErr = clip(err.Error())
	report := f.stalledLocked(now) && (f.st.reported.IsZero() || now.Sub(f.st.reported) >= f.o.StallReportEvery)
	if report {
		f.st.reported = now
	}
	st := f.st
	f.mu.Unlock()
	if !prev.reported.IsZero() && prev.id != id {
		f.o.Logger.Info("outbox: the event that stalled delivery is no longer pending; delivery moved on", "event", prev.id,
			"event_id", prev.eventID)
	}
	if !report {
		return
	}
	attrs := []any{"event", st.id, "event_id", st.eventID, "event_kind", st.kind, "failures", st.failures,
		"stalled_since", st.since.UTC().Format(time.RFC3339Nano), "err", st.lastErr}
	f.o.Logger.Error("outbox: delivery to the host's outbox table is stalled: the event at the head of the queue keeps failing "+
		"and every later event waits behind it (see the module documentation on a stalled forwarder)",
		append(attrs, f.backlog(ctx)...)...)
}

// progress records that delivery moves on: an event was delivered, or
// nothing is pending. A stall that was reported ends with an INFO line.
func (f *Forwarder) progress() {
	f.mu.Lock()
	prev := f.st
	f.st = stall{}
	f.mu.Unlock()
	if !prev.reported.IsZero() {
		f.o.Logger.Info("outbox: delivery to the host's outbox table resumed", "event", prev.id, "event_id", prev.eventID,
			"failures", prev.failures, "stalled_for", f.o.Now().Sub(prev.since).Round(time.Millisecond).String())
	}
}

// backlog returns the log attributes of the pending events: their number
// and the time of the oldest.
func (f *Forwarder) backlog(ctx context.Context) []any {
	var n, oldest int64
	if err := f.call(ctx, "count the pending events", func(ctx context.Context) (err error) {
		n, oldest, err = f.queue.PendingCount(ctx)
		return err
	}); err != nil {
		return []any{"pending_err", err.Error()}
	}
	attrs := []any{"pending", n}
	if n > 0 {
		attrs = append(attrs, "oldest_pending_at", time.UnixMicro(oldest).UTC().Format(time.RFC3339Nano))
	}
	return attrs
}

// Status returns the state of delivery: the backlog from the local outbox
// (ctx bounds the query) and whether delivery is stalled. When the backlog
// cannot be read, the error is returned with the rest of the state.
func (f *Forwarder) Status(ctx context.Context) (Status, error) {
	now := f.o.Now()
	var st Status
	f.mu.Lock()
	if f.stalledLocked(now) {
		st.Stalled, st.StalledSince, st.LastError = true, f.st.since.UTC(), f.st.lastErr
	}
	f.mu.Unlock()
	n, oldest, err := f.queue.PendingCount(ctx)
	if err != nil {
		return st, err
	}
	st.Pending = n
	if n > 0 {
		st.OldestPendingAt = time.UnixMicro(oldest).UTC()
	}
	return st, nil
}

// clip returns s as valid UTF-8 of at most MaxErrorBytes bytes.
func clip(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= MaxErrorBytes {
		return s
	}
	s = s[:MaxErrorBytes]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
