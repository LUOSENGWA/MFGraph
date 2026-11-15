package ports

import (
	"context"
	"time"
)

// Clock returns the current time. Business timestamps come from the Clock;
// lock and lease expiry (later slice) use database time instead.
type Clock interface {
	Now() time.Time
}

// ClockFunc adapts a function, such as time.Now, to Clock.
type ClockFunc func() time.Time

// Now calls f.
func (f ClockFunc) Now() time.Time { return f() }

// Event types written to the outbox by the first core slice.
const (
	EventSnapshotCreated  = "snapshot.created"
	EventSnapshotRestored = "snapshot.restored"
	EventBranchUpdated    = "branch.updated"
)

// Event is one outbox row (table outbox_events).
type Event struct {
	ID          string // event_id, a ULID
	Type        string
	WorkspaceID string
	AggregateID string // e.g. the snapshot_id
	Payload     []byte // canonical JSON
	CreatedAt   time.Time
}

// Tx is an open unit of work created by a Transactor. It is passed to every
// adapter that must write in the same database transaction (business tables
// and the outbox); adapters type-assert it to the concrete type of the
// Transactor they are wired with, and a Tx from another Transactor is an
// error.
type Tx interface {
	// TxKind names the implementation ("mysql", "memory"), for diagnostics.
	TxKind() string
}

// Transactor runs fn in one transaction: it commits when fn returns nil and
// rolls back otherwise.
type Transactor interface {
	WithTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}

// EventSink appends events to the outbox in the same transaction as the
// business writes, so that an event exists if and only if the change was
// committed. Publishing happens later (at least once).
type EventSink interface {
	Append(ctx context.Context, tx Tx, ev Event) error
}
