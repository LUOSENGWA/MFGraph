// Package outbox forwards the events of the local outbox (outbox_events) to
// the outbox table of a host platform, in module mode.
//
// A change writes its events to the local outbox in its own transaction
// (package core). The host gives the module a separate database connection
// for its outbox table, and two connections cannot commit in one
// transaction, so the Forwarder copies the events afterwards:
//
//   - strictly in outbox order, by (created_at_us, event_id): it delivers
//     the pending events one by one and stops at the first one it cannot
//     deliver, without skipping it; the next attempt starts with that event
//     again;
//   - at least once: an event whose event_id the host table already has
//     counts as delivered (the host's unique key deduplicates), so an event
//     that was written but not marked published is simply written again;
//   - every 500 ms, and at once when a change committed events (Notify);
//     after a failure the whole forwarder waits, 1 s at first and twice as
//     long after every further failure, up to 60 s;
//   - every database call (reading the local outbox, writing the host
//     table, marking a row) has its own time limit, 15 s by default: a call
//     that gets no answer is a failure like any other (logged, counted as a
//     retry of the event, followed by the backoff);
//   - events that were not delivered stay pending in the local outbox and
//     are delivered after a restart;
//   - an event that keeps failing stalls delivery: when it failed 5 times in
//     a row or for 5 minutes, the forwarder logs an ERROR line (the backlog,
//     the event's local ID and event_id, the error), again at most every 5
//     minutes, and an INFO line once delivery moves on; Status reports the
//     state for /readyz. An operator can repair the host table, or mark the
//     event skipped (status 'skipped' in outbox_events): only pending events
//     are delivered.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/actorref"
	"github.com/LUOSENGWA/MFGraph/internal/ids"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

// Defaults of Options.
const (
	DefaultInterval    = 500 * time.Millisecond
	DefaultBatch       = 100
	DefaultMinBackoff  = time.Second
	DefaultMaxBackoff  = 60 * time.Second
	DefaultCallTimeout = 15 * time.Second
)

// Row is a row of the host's outbox table.
type Row struct {
	EventID   string  // the envelope's event_id (64 lowercase hex digits)
	EventKind string  // for example mfgraph.commit_created
	ScopeID   string  // the project ID (scope_kind is always "project")
	ActorType *string // nil = NULL
	ActorID   string  // the complete actor reference
	Payload   []byte  // the envelope
	// CreatedAt is when the event occurred (the envelope's occurred_at);
	// AvailableAt and UpdatedAt are when the row is written. All UTC.
	CreatedAt   time.Time
	AvailableAt time.Time
	UpdatedAt   time.Time
}

// Sink is the host's outbox table.
type Sink interface {
	// Insert writes a row. A row whose event_id the table already has is
	// a success: the event was delivered before.
	Insert(ctx context.Context, row Row) error
	// Check reports whether the table can be reached (readiness).
	Check(ctx context.Context) error
}

// Options configure a Forwarder; zero values get the defaults.
type Options struct {
	Now        func() time.Time
	Logger     *slog.Logger
	Interval   time.Duration
	Batch      int
	MinBackoff time.Duration
	MaxBackoff time.Duration
	// CallTimeout bounds every database call of the forwarder.
	CallTimeout time.Duration
	// StallFailures, StallAfter and StallReportEvery: see
	// DefaultStallFailures.
	StallFailures    int
	StallAfter       time.Duration
	StallReportEvery time.Duration
}

// Forwarder copies the local outbox to a Sink (see the package
// documentation). Run it in one goroutine; Notify and Flush may be called
// from others.
type Forwarder struct {
	queue ports.OutboxQueue
	sink  Sink
	o     Options
	wake  chan struct{}
	// backoff is the current wait after failures (0 after a success);
	// only the goroutine of Run changes it.
	backoff time.Duration
	mu      sync.Mutex
	st      stall // the failures of the event at the head of the queue
}

// New returns a Forwarder from queue to sink.
func New(queue ports.OutboxQueue, sink Sink, o Options) *Forwarder {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	if o.Batch <= 0 {
		o.Batch = DefaultBatch
	}
	if o.MinBackoff <= 0 {
		o.MinBackoff = DefaultMinBackoff
	}
	if o.MaxBackoff < o.MinBackoff {
		o.MaxBackoff = max(DefaultMaxBackoff, o.MinBackoff)
	}
	if o.CallTimeout <= 0 {
		o.CallTimeout = DefaultCallTimeout
	}
	if o.StallFailures <= 0 {
		o.StallFailures = DefaultStallFailures
	}
	if o.StallAfter <= 0 {
		o.StallAfter = DefaultStallAfter
	}
	if o.StallReportEvery <= 0 {
		o.StallReportEvery = DefaultStallReportEvery
	}
	return &Forwarder{queue: queue, sink: sink, o: o, wake: make(chan struct{}, 1)}
}

// Notify wakes the forwarder: a change committed events. It never blocks.
func (f *Forwarder) Notify() {
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// nextBackoff is the wait after one more failure.
func (f *Forwarder) nextBackoff() time.Duration {
	return min(max(2*f.backoff, f.o.MinBackoff), f.o.MaxBackoff)
}

// Run delivers events until ctx is canceled.
func (f *Forwarder) Run(ctx context.Context) {
	for {
		n, err := f.Flush(ctx)
		if ctx.Err() != nil {
			return
		}
		wait := f.o.Interval
		switch {
		case err != nil:
			f.backoff = f.nextBackoff()
			wait = f.backoff
			f.o.Logger.Warn("outbox: delivery to the host's outbox failed; retrying", "retry_in", f.backoff.String(),
				"delivered", n, "err", err.Error())
		case n == f.o.Batch:
			f.backoff, wait = 0, 0 // more may be pending
		default:
			f.backoff = 0
		}
		if wait == 0 {
			continue
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-f.wake:
			if f.backoff == 0 {
				timer.Stop()
				break
			}
			// A failure: the whole forwarder waits out its backoff.
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}

// call runs one database call of the forwarder within CallTimeout. A call
// that runs out of time fails with an error that says so.
func (f *Forwarder) call(ctx context.Context, what string, fn func(context.Context) error) error {
	cctx, cancel := context.WithTimeout(ctx, f.o.CallTimeout)
	defer cancel()
	err := fn(cctx)
	if err != nil && ctx.Err() == nil && errors.Is(cctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%s: no answer within %s (%w)", what, f.o.CallTimeout, err)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

// Flush delivers one batch of pending events, in order, and stops at the
// first event it cannot deliver (whose retry it records). It returns how
// many events it delivered.
func (f *Forwarder) Flush(ctx context.Context) (int, error) {
	var evs []ports.Event
	if err := f.call(ctx, "read the local outbox", func(ctx context.Context) (err error) {
		evs, err = f.queue.PendingEvents(ctx, f.o.Batch)
		return err
	}); err != nil {
		if ctx.Err() == nil {
			f.failed(ctx, nil, "", err)
		}
		return 0, err
	}
	if len(evs) == 0 {
		f.progress()
	}
	for i, ev := range evs {
		now := f.o.Now().UTC()
		row, err := RowOf(ev, now)
		if err == nil {
			err = f.call(ctx, "write the host's outbox table", func(ctx context.Context) error { return f.sink.Insert(ctx, row) })
		}
		if err != nil {
			if ctx.Err() == nil {
				next := now.Add(f.nextBackoff()).UnixMicro()
				if merr := f.call(ctx, "record a failed delivery", func(ctx context.Context) error {
					return f.queue.MarkFailed(ctx, ev.ID, next)
				}); merr != nil {
					f.o.Logger.Warn("outbox: could not record a failed delivery", "event", ev.ID, "err", merr.Error())
				}
			}
			err = fmt.Errorf("event %s (%s): %w", ev.ID, ev.Type, err)
			if ctx.Err() == nil {
				f.failed(ctx, &ev, row.EventID, err)
			}
			return i, err
		}
		if err := f.call(ctx, "mark the event published", func(ctx context.Context) error {
			return f.queue.MarkPublished(ctx, ev.ID, now.UnixMicro())
		}); err != nil {
			// Delivered but not marked: it is delivered again later, which
			// the host's unique key absorbs.
			err = fmt.Errorf("event %s (%s): %w", ev.ID, ev.Type, err)
			if ctx.Err() == nil {
				f.failed(ctx, &ev, row.EventID, err)
			}
			return i, err
		}
		f.progress()
	}
	return len(evs), nil
}

var (
	hexIDRe    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	actorTypRe = regexp.MustCompile(`^[a-z0-9_-]{1,16}$`)
)

// ErrBadEnvelope: an outbox row whose payload is not a valid envelope. The
// forwarder does not skip it (the order would break); it is reported.
var ErrBadEnvelope = errors.New("outbox: not a valid event envelope")

// RowOf returns the host row of an outbox event, written at now.
func RowOf(ev ports.Event, now time.Time) (Row, error) {
	var env struct {
		V          int    `json:"v"`
		EventID    string `json:"event_id"`
		OccurredAt string `json:"occurred_at"`
		ProjectID  string `json:"project_id"`
		Actor      struct {
			Type *string `json:"type"`
			ID   string  `json:"id"`
		} `json:"actor"`
	}
	if err := json.Unmarshal(ev.Payload, &env); err != nil {
		return Row{}, fmt.Errorf("%w: %v", ErrBadEnvelope, err)
	}
	occurred, err := time.Parse(time.RFC3339Nano, env.OccurredAt)
	switch {
	case env.V != 1:
		return Row{}, fmt.Errorf("%w: version %d", ErrBadEnvelope, env.V)
	case !hexIDRe.MatchString(env.EventID):
		return Row{}, fmt.Errorf("%w: event_id", ErrBadEnvelope)
	case err != nil:
		return Row{}, fmt.Errorf("%w: occurred_at", ErrBadEnvelope)
	case !ids.Valid(env.ProjectID):
		return Row{}, fmt.Errorf("%w: project_id", ErrBadEnvelope)
	case !actorref.Valid(env.Actor.ID):
		return Row{}, fmt.Errorf("%w: actor.id", ErrBadEnvelope)
	case env.Actor.Type != nil && !actorTypRe.MatchString(*env.Actor.Type):
		return Row{}, fmt.Errorf("%w: actor.type", ErrBadEnvelope)
	}
	now = now.UTC()
	return Row{EventID: env.EventID, EventKind: ev.Type, ScopeID: env.ProjectID, ActorType: env.Actor.Type, ActorID: env.Actor.ID,
		Payload: ev.Payload, CreatedAt: occurred.UTC(), AvailableAt: now, UpdatedAt: now}, nil
}
